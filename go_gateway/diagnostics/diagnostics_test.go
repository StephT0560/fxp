// SPDX-License-Identifier: MIT
package diagnostics

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOptInTraceAndMonotonicClock(t *testing.T) {
	t.Setenv("FXP_HOP_TRACE", "")
	t.Setenv("FXP_PROFILE_DIR", "")
	t.Setenv("FXP_TRACE_FILE", "")
	stop, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	if Enabled() {
		t.Fatal("enabled by default")
	}
	stop()
	path := filepath.Join(t.TempDir(), "hops.jsonl")
	t.Setenv("FXP_HOP_TRACE", path)
	stop, err = Start()
	if err != nil {
		t.Fatal(err)
	}
	Event("first", "id")
	Event("second", "id")
	stop()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var events []struct {
		Stage string `json:"stage"`
		NS    int64  `json:"ns"`
	}
	for _, line := range splitLines(data) {
		var e struct {
			Stage string `json:"stage"`
			NS    int64  `json:"ns"`
		}
		if err = json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	if len(events) != 2 || events[0].NS <= 0 || events[1].NS < events[0].NS {
		t.Fatalf("bad trace: %s", data)
	}
}
func splitLines(b []byte) [][]byte { return bytes.Split(bytes.TrimSpace(b), []byte("\n")) }
