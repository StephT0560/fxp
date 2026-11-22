// SPDX-License-Identifier: MIT
package transport

import "testing"

func TestTraderSessionsCountsTradingSessionsOnly(t *testing.T) {
	ids := []string{"presence-a1", "presence-a2", "presence-b1", "presence-view"}
	t.Cleanup(func() {
		for _, id := range ids {
			sessionStore.Delete(id)
			sessionReadOnly.Delete(id)
		}
	})
	sessionStore.Store(ids[0], "ws:alpha")
	sessionStore.Store(ids[1], "ws:alpha")
	sessionStore.Store(ids[2], "ws:beta")
	sessionStore.Store(ids[3], "ws:viewer")
	sessionReadOnly.Store(ids[3], struct{}{})
	got := map[string]int{}
	for _, s := range TraderSessions() {
		got[s.Trader] = s.Sessions
	}
	if got["ws:alpha"] != 2 || got["ws:beta"] != 1 || got["ws:viewer"] != 0 {
		t.Fatalf("unexpected sessions %v", got)
	}
}
