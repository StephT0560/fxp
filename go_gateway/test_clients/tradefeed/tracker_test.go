// SPDX-License-Identifier: MIT

package tradefeed

import "testing"

func TestTrackerObservesSequencesPerSymbol(t *testing.T) {
	var tracker Tracker
	if gap, expected := tracker.Observe("A", 1); gap || expected != 1 {
		t.Fatalf("first A sequence = (%v, %d), want (false, 1)", gap, expected)
	}
	if gap, expected := tracker.Observe("B", 1); gap || expected != 1 {
		t.Fatalf("first B sequence = (%v, %d), want (false, 1)", gap, expected)
	}
	if gap, expected := tracker.Observe("A", 2); gap || expected != 2 {
		t.Fatalf("second A sequence = (%v, %d), want (false, 2)", gap, expected)
	}
	if gap, expected := tracker.Observe("A", 4); !gap || expected != 3 {
		t.Fatalf("gapped A sequence = (%v, %d), want (true, 3)", gap, expected)
	}
	if gap, expected := tracker.Observe("A", 5); gap || expected != 5 {
		t.Fatalf("post-gap A sequence = (%v, %d), want (false, 5)", gap, expected)
	}
}
