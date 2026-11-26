// SPDX-License-Identifier: MIT

package transport

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
)

func TestSessionDeadlineIsOptInAndClearedOnDisconnect(t *testing.T) {
	claims := &jwtClaims{Sub: "deadline-test", Exp: time.Now().Unix() + 60}
	logon := &protobuf.Logon{}
	t.Setenv("FXP_JWT_ENFORCE_SESSION_EXPIRY", "false")
	sid := startSession(logon, claims)
	defer invalidateSession(sid)
	if deadline := sessionDeadline(sid); !deadline.IsZero() {
		t.Fatalf("enforcement disabled, got deadline %v", deadline)
	}

	t.Setenv("FXP_JWT_ENFORCE_SESSION_EXPIRY", "true")
	enforced := startSession(logon, claims)
	defer invalidateSession(enforced)
	if got, want := sessionDeadline(enforced), time.Unix(claims.Exp, 0); !got.Equal(want) {
		t.Fatalf("session deadline = %v, want %v", got, want)
	}
	endSession(enforced)
	if deadline := sessionDeadline(enforced); !deadline.IsZero() {
		t.Fatalf("disconnect retained deadline %v", deadline)
	}
}

type sessionWire struct {
	send  func(*protobuf.FXPMessage)
	read  func() (*protobuf.FXPMessage, error)
	close func()
}

func newSessionWire(t *testing.T, transport string) sessionWire {
	t.Helper()
	done := make(chan struct{})
	if transport == "tcp" {
		server, client := pipes(t)
		go func() { defer close(done); handleTCPClient(server) }()
		return sessionWire{
			send: func(msg *protobuf.FXPMessage) { writeMsg(t, client, msg) },
			read: func() (*protobuf.FXPMessage, error) {
				_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
				raw, err := readFrame(client, 1<<20)
				msg := &protobuf.FXPMessage{}
				if err == nil {
					err = proto.Unmarshal(raw, msg)
				}
				return msg, err
			},
			close: func() { _ = client.Close(); <-done },
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		handleConnections(w, r)
	}))
	client, _, err := websocket.DefaultDialer.Dial(wsURL(server), nil)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return sessionWire{
		send: func(msg *protobuf.FXPMessage) {
			raw, err := proto.Marshal(msg)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.WriteMessage(websocket.BinaryMessage, raw); err != nil {
				t.Fatal(err)
			}
		},
		read: func() (*protobuf.FXPMessage, error) {
			_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, raw, err := client.ReadMessage()
			msg := &protobuf.FXPMessage{}
			if err == nil {
				err = proto.Unmarshal(raw, msg)
			}
			return msg, err
		},
		close: func() { _ = client.Close(); <-done; server.Close() },
	}
}

func sessionToken(t *testing.T, subject string, exp int64, id string) string {
	t.Helper()
	return signJWT(t, "session-test", map[string]any{"alg": "HS256"}, map[string]any{
		"sub": subject, "exp": exp, "iat": time.Now().Unix(), "iss": DefaultJWTIssuer, "aud": DefaultJWTAudience, "jti": id,
	})
}

func wireLogon(t *testing.T, wire sessionWire, token string, cod, accepted bool) {
	t.Helper()
	msg := logonMsg(token, "")
	msg.GetLogon().CancelOnDisconnect = cod
	wire.send(msg)
	reply, err := wire.read()
	if err != nil || reply.GetLogonAck() == nil || reply.GetLogonAck().Accepted != accepted {
		t.Fatalf("logon response = %v, %v, want accepted=%t", reply, err, accepted)
	}
}

func TestRejectedRenewalPreservesSessionAndReconnectToken(t *testing.T) {
	for _, transport := range []string{"tcp", "ws"} {
		t.Run(transport, func(t *testing.T) {
			t.Setenv("FXP_JWT_SECRET", "session-test")
			t.Setenv("FXP_JWT_REPLAY_CACHE", "true")
			resetReplayCache()
			t.Cleanup(resetReplayCache)
			wire := newSessionWire(t, transport)
			defer wire.close()
			exp := time.Now().Unix() + 60
			wireLogon(t, wire, sessionToken(t, "renewal", exp, "initial"), false, true)
			changed := sessionToken(t, "other-trader", exp, "changed")
			wireLogon(t, wire, changed, false, false)
			policy := sessionToken(t, "renewal", exp, "policy")
			wireLogon(t, wire, policy, true, false)
			wireLogon(t, wire, sessionToken(t, "renewal", exp, "renewal"), false, true)
			for _, token := range []string{changed, policy} {
				reconnected := newSessionWire(t, transport)
				wireLogon(t, reconnected, token, false, true)
				reconnected.close()
			}
			if _, _, err := HandleLogon(&protobuf.Logon{Token: policy}); err == nil {
				t.Fatal("accepted reconnect token was reusable")
			}
		})
	}
}

func TestSessionExpiryClosesOutputDuringCoreBackpressure(t *testing.T) {
	for _, transport := range []string{"tcp", "ws"} {
		t.Run(transport, func(t *testing.T) {
			t.Setenv("FXP_JWT_SECRET", "session-test")
			t.Setenv("FXP_JWT_ENFORCE_SESSION_EXPIRY", "true")
			oldMD, oldCancel := mdConn, cancelOnDisconnect
			server, core := pipes(t)
			mdConn = newPooledConnWith(0, server)
			canceled := make(chan string, 2)
			cancelOnDisconnect = func(owner string) { canceled <- owner }
			wire := newSessionWire(t, transport)
			defer func() { _ = core.Close(); wire.close(); mdConn, cancelOnDisconnect = oldMD, oldCancel }()
			symbol := "EXPIRY-" + transport
			wireLogon(t, wire, sessionToken(t, "expiring", time.Now().Unix()+2, "expiry"), true, true)
			wire.send(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_MarketDataRequest{MarketDataRequest: &protobuf.MarketDataRequest{Symbols: []string{symbol}}}})
			waitUntil(t, func() bool { _, ok := clientSubs.Load(symbol); return ok })
			if _, err := wire.read(); err == nil {
				t.Fatal("expired connection still open")
			}
			select {
			case owner := <-canceled:
				if owner != "expiring" {
					t.Fatalf("canceled %s", owner)
				}
			case <-time.After(time.Second):
				t.Fatal("expiry did not trigger cancel-on-disconnect")
			}
			if clients, ok := clientSubs.Load(symbol); ok {
				clients.(*sync.Map).Range(func(_, _ any) bool { t.Error("expired subscriber retained"); return true })
			}
		})
	}
}

func TestRenewalReplacesIdleExpiryTimer(t *testing.T) {
	for _, transport := range []string{"tcp", "ws"} {
		t.Run(transport, func(t *testing.T) {
			t.Setenv("FXP_JWT_SECRET", "session-test")
			t.Setenv("FXP_JWT_ENFORCE_SESSION_EXPIRY", "true")
			wire := newSessionWire(t, transport)
			defer wire.close()
			exp := time.Now().Unix() + 2
			wireLogon(t, wire, sessionToken(t, "renewed", exp, "first"), false, true)
			wireLogon(t, wire, sessionToken(t, "renewed", exp+3, "second"), false, true)
			time.Sleep(time.Until(time.Unix(exp, 0)) + 50*time.Millisecond)
			wireLogon(t, wire, sessionToken(t, "renewed", exp+3, fmt.Sprint(exp)), false, true)
			if _, err := wire.read(); err == nil {
				t.Fatal("idle renewed session did not expire")
			}
		})
	}
}
