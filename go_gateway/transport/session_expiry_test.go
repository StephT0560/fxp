// SPDX-License-Identifier: MIT

package transport

import (
	"testing"
	"time"

	"go_gateway/protobuf"
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
