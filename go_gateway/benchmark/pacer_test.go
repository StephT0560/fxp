// SPDX-License-Identifier: MIT
package main

import (
	"testing"
	"time"
)

func TestPacersPreserveDeadline(t *testing.T) {
	for _, mode := range []string{"sleep", "busy-spin", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			deadline := time.Now().Add(2 * time.Millisecond)
			paceUntil(deadline, mode)
			if time.Now().Before(deadline) {
				t.Fatal("pacer sent early")
			}
			paceUntil(time.Now().Add(-time.Second), mode)
		})
	}
}
func TestRejectUnknownPacer(t *testing.T) {
	c := config{Mode: "open", Pacer: "typo", Concurrency: 1, Orders: 2, Warmup: 2, Repetitions: 1, Rate: 1000, Timeout: time.Second, Symbols: "single", Symbol: "TEST", Quantity: 1}
	if validate(c) == nil {
		t.Fatal("unknown pacer accepted")
	}
}
func TestAttributionKeepsPairedSamples(t *testing.T) {
	c := config{Mode: "open", Pacer: "sleep", Rate: 1000, Orders: 10, Timeout: time.Second, Quantity: 1}
	r, err := fakePhase(t, c, 30*time.Millisecond, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.SendLag.TotalCount() != 10 || r.AckAfterSend.TotalCount() != 10 {
		t.Fatal("missing attribution samples")
	}
	if r.Ack.ValueAtQuantile(50) < int64(20*time.Millisecond) {
		t.Fatal("intended latency lost")
	}
}
