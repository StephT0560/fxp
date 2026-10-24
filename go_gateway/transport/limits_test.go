// SPDX-License-Identifier: MIT

package transport

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go_gateway/protobuf"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

func withLimits(t *testing.T, mut func(*Limits)) {
	t.Helper()
	oldL, oldT, oldW := limitsP.Load(), tcpConnsP.Load(), wsConnsP.Load()
	l := *oldL
	mut(&l)
	setLimits(l)
	t.Cleanup(func() { limitsP.Store(oldL); tcpConnsP.Store(oldT); wsConnsP.Store(oldW) })
}

func testJWT(t *testing.T, sub string) string {
	t.Helper()
	t.Setenv("FXP_JWT_SECRET", "test-secret")
	enc := base64.RawURLEncoding.EncodeToString
	now := time.Now().Unix()
	head := enc([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body := enc([]byte(fmt.Sprintf(`{"sub":%q,"iss":%q,"aud":%q,"exp":%d,"iat":%d}`, sub, DefaultJWTIssuer, DefaultJWTAudience, now+3600, now)))
	mac := hmac.New(sha256.New, []byte("test-secret"))
	mac.Write([]byte(head + "." + body))
	return head + "." + body + "." + enc(mac.Sum(nil))
}

func logonMsg(token, sid string) *protobuf.FXPMessage {
	return &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_Logon{Logon: &protobuf.Logon{Token: token, SessionId: sid}}}
}

func heartbeat(pad int) *protobuf.FXPMessage {
	return &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_Heartbeat{Heartbeat: &protobuf.Heartbeat{Status: strings.Repeat("x", pad)}}}
}

func writeMsg(t *testing.T, c net.Conn, m *protobuf.FXPMessage) {
	t.Helper()
	raw, _ := proto.Marshal(m)
	frame := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(frame, uint32(len(raw)))
	copy(frame[4:], raw)
	_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write(frame); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readMsg(t *testing.T, c net.Conn) *protobuf.FXPMessage {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	body, err := readFrame(c, 1<<20)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	m := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(body, m); err != nil {
		t.Fatal(err)
	}
	return m
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// expectClosed drains c until the server closes it.
func expectClosed(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.Copy(io.Discard, c); isTimeout(err) {
		t.Fatal("connection not closed by server")
	}
}

func expectOpen(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := c.Read(make([]byte, 1)); !isTimeout(err) {
		t.Fatalf("connection closed or unexpected data: %v", err)
	}
}

func tcpClient(t *testing.T) net.Conn {
	srv, cli := pipes(t)
	go handleTCPClient(srv)
	return cli
}

func authedTCPClient(t *testing.T) net.Conn {
	c := tcpClient(t)
	writeMsg(t, c, logonMsg(testJWT(t, "TRADER"), "sid"))
	if !readMsg(t, c).GetLogonAck().GetAccepted() {
		t.Fatal("logon rejected")
	}
	return c
}

func TestTCPPreAuthFrameLimitCheckedBeforeAllocation(t *testing.T) {
	withLimits(t, func(l *Limits) { l.MaxFramePreAuth = 4096 })
	c := tcpClient(t)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 4097)
	_, _ = c.Write(hdr[:])
	expectClosed(t, c)
}

func TestTCPPostAuthFrameLimit(t *testing.T) {
	withLimits(t, func(l *Limits) { l.MaxFramePreAuth, l.MaxFrame = 4096, 65536 })
	c := authedTCPClient(t)
	writeMsg(t, c, heartbeat(10_000)) // above the pre-auth cap, accepted after Logon
	expectOpen(t, c)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 65537)
	_, _ = c.Write(hdr[:])
	expectClosed(t, c)
}

func TestTCPHandshakeTimeout(t *testing.T) {
	withLimits(t, func(l *Limits) { l.HandshakeTimeout = 100 * time.Millisecond })
	expectClosed(t, tcpClient(t))
}

func TestTCPRateLimit(t *testing.T) {
	withLimits(t, func(l *Limits) { l.RatePerSec, l.RateBurst = 0.001, 2 })
	c := authedTCPClient(t) // Logon uses the first token
	writeMsg(t, c, heartbeat(0))
	writeMsg(t, c, heartbeat(0))
	if e := readMsg(t, c).GetErrorMessage(); e.GetErrorType() != protobuf.ErrorType_RATE_LIMIT_EXCEEDED {
		t.Fatalf("got %v, want RATE_LIMIT_EXCEEDED", e)
	}
}

func TestTCPMaxConnections(t *testing.T) {
	withLimits(t, func(l *Limits) { l.MaxConns = 1 })
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go serveTCP(l)
	c1, _ := net.Dial("tcp", l.Addr().String())
	expectOpen(t, c1)
	c2, _ := net.Dial("tcp", l.Addr().String())
	defer c2.Close()
	expectClosed(t, c2)
	c1.Close()
	time.Sleep(50 * time.Millisecond)
	c3, _ := net.Dial("tcp", l.Addr().String())
	defer c3.Close()
	expectOpen(t, c3)
}

// Client-chosen session ids must not let one connection take over another's.
func TestSessionIDsAreServerGenerated(t *testing.T) {
	ids := map[string]string{}
	for _, trader := range []string{"ALICE", "MALLORY"} {
		srv, cli := routedPipes(t)
		go func() { _, _ = io.Copy(io.Discard, cli) }()
		sid := ""
		HandleFXPMessageAuthenticated(srv, logonMsg(testJWT(t, trader), "shared-id"), &sid)
		if sid == "" || sid == "shared-id" {
			t.Fatalf("%s: session id %q is client-chosen", trader, sid)
		}
		ids[trader] = sid
	}
	if ids["ALICE"] == ids["MALLORY"] || GetTraderID(ids["ALICE"]) != "ALICE" || GetTraderID(ids["MALLORY"]) != "MALLORY" {
		t.Fatalf("sessions collide: %v", ids)
	}
}

func TestTCPDisconnectInvalidatesSession(t *testing.T) {
	before := 0
	sessionStore.Range(func(_, _ any) bool { before++; return true })
	c := authedTCPClient(t)
	c.Close()
	waitUntil(t, func() bool {
		n := 0
		sessionStore.Range(func(_, _ any) bool { n++; return true })
		return n == before
	})
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func wsURL(srv *httptest.Server) string { return "ws" + strings.TrimPrefix(srv.URL, "http") }

func wsExpectClosed(t *testing.T, c *websocket.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			if isTimeout(err) {
				t.Fatal("websocket not closed by server")
			}
			return
		}
	}
}

func TestWSMaxConnections(t *testing.T) {
	withLimits(t, func(l *Limits) { l.MaxConns = 1 })
	srv := httptest.NewServer(http.HandlerFunc(handleConnections))
	defer srv.Close()
	c1, _, err := websocket.DefaultDialer.Dial(wsURL(srv), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	c2, resp, err := websocket.DefaultDialer.Dial(wsURL(srv), nil)
	if err == nil {
		c2.Close()
		t.Fatal("second websocket accepted beyond MaxConns")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("over-limit response: %v", resp)
	}
}

func TestWSHandshakeTimeoutAndPreAuthLimit(t *testing.T) {
	withLimits(t, func(l *Limits) { l.HandshakeTimeout, l.MaxFramePreAuth = 100*time.Millisecond, 4096 })
	srv := httptest.NewServer(http.HandlerFunc(handleConnections))
	defer srv.Close()

	idle, _, err := websocket.DefaultDialer.Dial(wsURL(srv), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	wsExpectClosed(t, idle)

	big, _, err := websocket.DefaultDialer.Dial(wsURL(srv), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer big.Close()
	_ = big.WriteMessage(websocket.BinaryMessage, []byte(strings.Repeat("\xff", 5000)))
	wsExpectClosed(t, big)
}
