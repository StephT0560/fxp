//go:build scenariointegration

// SPDX-License-Identifier: MIT
package main

import (
	"reflect"
	"testing"
	"time"
)

func TestRealScenariosRepeat(t *testing.T) {
	cfg, e := configFromEnv()
	if e != nil {
		t.Fatal(e)
	}
	for _, info := range scenarioCatalog {
		t.Run(info.ID, func(t *testing.T) {
			var previous []string
			m := scenarioManager{}
			for repetition := 0; repetition < 2; repetition++ {
				r, e := m.start(cfg, info.ID, 71)
				if e != nil {
					t.Fatal(e)
				}
				select {
				case <-r.done:
				case <-time.After(95 * time.Second):
					t.Fatal("scenario timeout")
				}
				var outcomes []string
				for _, event := range r.events {
					if event.Kind == "step" && event.Status == "passed" {
						outcomes = append(outcomes, event.Outcome)
					}
				}
				if r.State != "passed" {
					for _, ev := range r.events {
						if ev.Kind != "frame" {
							t.Logf("%+v", ev)
						}
					}
					t.Fatalf("scenario failed: %s", r.State)
				}
				if repetition == 1 && !reflect.DeepEqual(previous, outcomes) {
					t.Fatalf("same seed changed outcomes: %v vs %v", previous, outcomes)
				}
				previous = outcomes
			}
		})
	}
}
