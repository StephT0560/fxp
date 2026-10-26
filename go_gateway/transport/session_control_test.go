// SPDX-License-Identifier: MIT
package transport

import (
	"encoding/binary"
	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
	"io"
	"net"
	"testing"
	"time"
)

func readFXP(t *testing.T, c net.Conn) *protobuf.FXPMessage {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var h [4]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, binary.BigEndian.Uint32(h[:]))
	if _, err := io.ReadFull(c, body); err != nil {
		t.Fatal(err)
	}
	var msg protobuf.FXPMessage
	if err := proto.Unmarshal(body, &msg); err != nil {
		t.Fatal(err)
	}
	return &msg
}

func sessionControlMsg(id string) *protobuf.FXPMessage {
	return &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_SessionControl{SessionControl: &protobuf.SessionControl{RequestId: id, Sender: "spoofed-operator", OpeningCallSeconds: 30}}}
}

func TestSessionControlRequiresOperatorRole(t *testing.T) {
	srv, cli := routedPipes(t)
	session := "sc-demo-user"
	sessionStore.Store(session, "ALICE")
	defer invalidateSession(session)
	go HandleFXPMessageAuthenticated(srv, sessionControlMsg("R1"), &session)
	got := readFXP(t, cli).GetSessionControlReport()
	if got == nil || got.RequestId != "R1" || got.Sender != "ALICE" || got.RejectReason != "operator role required" {
		t.Fatalf("expected operator reject, got %+v", got)
	}
}

func TestSessionControlReportRoutesBackToTheOperator(t *testing.T) {
	srv, cli := routedPipes(t)
	key, err := registerSessionControlRoute(srv, &protobuf.SessionControl{RequestId: "R2", Sender: "OPS"})
	if err != nil {
		t.Fatal(err)
	}
	if key == "R2" {
		t.Fatal("core-facing id must be gateway-scoped")
	}
	go routeFromCore(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_SessionControlReport{SessionControlReport: &protobuf.SessionControlReport{RequestId: key, Sender: "OPS", SymbolsAffected: 10}}})
	got := readFXP(t, cli).GetSessionControlReport()
	if got == nil || got.RequestId != "R2" || got.SymbolsAffected != 10 {
		t.Fatalf("unexpected report %+v", got)
	}
}

func TestOperatorRoleComesOnlyFromTheToken(t *testing.T) {
	const secret = "session-control-test-secret-0123456789"
	t.Setenv("FXP_JWT_SECRET", secret)
	t.Setenv("FXP_JWT_ISSUER", "fxp-test")
	t.Setenv("FXP_JWT_AUDIENCE", "fxp-gw-test")
	now := time.Now().Unix()
	for i, tc := range []struct {
		role     any
		operator bool
	}{{"operator", true}, {nil, false}, {"admin", false}} {
		claims := map[string]any{"sub": "T1", "iss": "fxp-test", "aud": "fxp-gw-test", "iat": now, "exp": now + 60, "jti": "sc-" + string(rune('a'+i))}
		if tc.role != nil {
			claims["fxp_role"] = tc.role
		}
		token := signJWT(t, secret, map[string]any{"alg": "HS256", "typ": "JWT"}, claims)
		_, session, err := HandleLogon(&protobuf.Logon{Token: token})
		if err != nil {
			t.Fatal(err)
		}
		if IsOperator(session) != tc.operator {
			t.Fatalf("role %v: operator=%v", tc.role, IsOperator(session))
		}
		invalidateSession(session)
		if IsOperator(session) {
			t.Fatal("operator flag must not outlive the session")
		}
	}
}
