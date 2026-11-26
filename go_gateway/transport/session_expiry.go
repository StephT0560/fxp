// SPDX-License-Identifier: MIT

package transport

import (
	"fmt"
	"time"

	"go_gateway/protobuf"
)

// Serialize renewal with expiry so an old timer cannot close a renewed session.
func startConnectionSession(conn any, logon *protobuf.Logon, claims *jwtClaims, previous string) (string, error) {
	value, ok := clientWriters.Load(conn)
	if !ok {
		return "", fmt.Errorf("connection closed")
	}
	writer := value.(*clientWriter)
	writer.sessionMu.Lock()
	defer writer.sessionMu.Unlock()
	select {
	case <-writer.stopped:
		return "", fmt.Errorf("connection closed")
	default:
	}
	if deadline := sessionDeadline(previous); !deadline.IsZero() && !time.Now().Before(deadline) {
		return "", fmt.Errorf("session expired")
	}
	if time.Now().Unix() >= claims.Exp {
		return "", fmt.Errorf("token expired")
	}
	if err := claimJWT(claims); err != nil {
		return "", err
	}
	if writer.expiryTimer != nil {
		writer.expiryTimer.Stop()
	}
	if previous != "" {
		sessionCOD.Delete(previous)
		invalidateSession(previous)
	}
	sid := startSession(logon, claims)
	if deadline := sessionDeadline(sid); !deadline.IsZero() {
		writer.expiryTimer = time.AfterFunc(time.Until(deadline), func() {
			writer.sessionMu.Lock()
			if !IsAuthenticated(sid) {
				writer.sessionMu.Unlock()
				return
			}
			// Close output before cleanup, which may block on a Core write.
			writer.forceStop()
			writer.sessionMu.Unlock()
			// The reader owns session teardown after its in-flight forwarding
			// completes. It also preserves the Core-loss disconnect cause.
		})
	}
	return sid, nil
}

func (w *clientWriter) stopSessionExpiry() {
	w.sessionMu.Lock()
	defer w.sessionMu.Unlock()
	if w.expiryTimer != nil {
		w.expiryTimer.Stop()
		w.expiryTimer = nil
	}
}
