// SPDX-License-Identifier: MIT
package main

import (
	"testing"
	"time"
)

func TestPhasePlan(t *testing.T) {
	plan := phasePlan(100*time.Second, 20*time.Second)
	if len(plan) != 2 || plan[0].ph != PhaseOpenAuction || plan[0].d != 20*time.Second || plan[1].ph != PhaseContinuous || plan[1].d != 80*time.Second {
		t.Fatalf("opening-auction plan %+v", plan)
	}
	day := phasePlan(100*time.Second, 0)
	if len(day) != 5 || day[0].ph != PhasePreMarket || day[1].ph != PhaseOpenAuction {
		t.Fatalf("default plan %+v", day)
	}
	var total time.Duration
	for _, p := range day {
		total += p.d
	}
	if total != 100*time.Second {
		t.Fatalf("default plan lasts %v", total)
	}
}
