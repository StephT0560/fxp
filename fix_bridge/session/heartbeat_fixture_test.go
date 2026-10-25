// SPDX-License-Identifier: MIT
package session

import (
	"testing"
	"time"
)

func TestHeartbeatTimingOverrideDoesNotLeakAcrossSessionCleanup(t *testing.T) {
	fastHeartbeats(t)
	p := newPeer(t)
	heartbeatUnit = time.Second // A subsequent cleanup restores the shared fixture.
	p.logon("108", "1")
	if got := p.sess.heartbeatInterval(); got != 20*time.Millisecond {
		t.Fatalf("session observed restored fixture: got %s, want 20ms", got)
	}
}
