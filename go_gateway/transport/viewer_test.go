// SPDX-License-Identifier: MIT
package transport

import (
	"go_gateway/protobuf"
	"testing"
	"time"
)

func TestReadOnlyClaimOverridesOperatorAndCancelOnDisconnect(t *testing.T) {
	secret := "synthetic-viewer-test-key-000000000000"
	t.Setenv("FXP_JWT_SECRET", secret)
	t.Setenv("FXP_JWT_ISSUER", "viewer-test")
	t.Setenv("FXP_JWT_AUDIENCE", "viewer-test")
	now := time.Now().Unix()
	token := signJWT(t, secret, map[string]any{"alg": "HS256"}, map[string]any{"sub": "viewer-test", "iat": now, "exp": now + 60, "iss": "viewer-test", "aud": "viewer-test:viewer-v1", "fxp_role": "operator", "fxp_read_only": true})
	_, sid, err := HandleLogon(&protobuf.Logon{Token: token, CancelOnDisconnect: true})
	if err != nil {
		t.Fatal("viewer logon failed")
	}
	defer invalidateSession(sid)
	if IsOperator(sid) {
		t.Error("read-only token gained operator authority")
	}
	if _, ok := sessionCOD.Load(sid); ok {
		t.Error("viewer armed cancel-on-disconnect")
	}
}

func TestViewerMutationsAreRejectedBeforeRouting(t *testing.T) {
	for _, message := range []*protobuf.FXPMessage{
		{Payload: &protobuf.FXPMessage_NewOrder{NewOrder: &protobuf.NewOrderSingle{OrderId: "viewer-new"}}},
		{Payload: &protobuf.FXPMessage_OrderCancel{OrderCancel: &protobuf.OrderCancelRequest{OrderId: "viewer-cancel"}}},
		{Payload: &protobuf.FXPMessage_OrderCancelReplace{OrderCancelReplace: &protobuf.OrderCancelReplaceRequest{OrderId: "viewer-replace"}}},
		{Payload: &protobuf.FXPMessage_OrderMassCancel{OrderMassCancel: &protobuf.OrderMassCancelRequest{}}},
		sessionControlMsg("viewer-control"),
	} {
		srv, cli := routedPipes(t)
		sid := "readonly-mutations"
		sessionStore.Store(sid, "viewer")
		sessionReadOnly.Store(sid, struct{}{})
		done := make(chan struct{})
		go func() { HandleFXPMessageAuthenticated(srv, message, &sid); close(done) }()
		got := readFXP(t, cli).GetErrorMessage()
		if got == nil || got.ErrorMessage != "read_only_session" {
			t.Fatal("viewer mutation not rejected")
		}
		<-done
		invalidateSession(sid)
	}
}
func TestViewerAudienceCannotAuthenticateAsWritable(t *testing.T) {
	secret := "synthetic-viewer-test-key-000000000000"
	t.Setenv("FXP_JWT_SECRET", secret)
	t.Setenv("FXP_JWT_ISSUER", "viewer-test")
	t.Setenv("FXP_JWT_AUDIENCE", "viewer-test")
	for _, tc := range []struct {
		readOnly bool
		aud      string
		valid    bool
	}{{true, "viewer-test:viewer-v1", true}, {false, "viewer-test:viewer-v1", false}, {true, "viewer-test", false}, {false, "viewer-test", true}} {
		now := time.Now().Unix()
		token := signJWT(t, secret, map[string]any{"alg": "HS256"}, map[string]any{"sub": "viewer-test", "iat": now, "exp": now + 60, "iss": "viewer-test", "aud": tc.aud, "fxp_read_only": tc.readOnly})
		_, err := validateJWT(token)
		if (err == nil) != tc.valid {
			t.Fatal("viewer audience did not enforce its capability")
		}
	}
}
