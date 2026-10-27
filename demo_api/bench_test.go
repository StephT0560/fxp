// SPDX-License-Identifier: MIT
package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestBenchCheckedInAndReadOnly(t *testing.T) {
	data, e := readConsoleAsset("benchmarks/console.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = validateBench(data); e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	(&app{}).bench(w, httptest.NewRequest("GET", "/api/bench", nil))
	if w.Code != 200 || w.Body.String() != string(data) {
		t.Fatalf("benchmark not served verbatim: %d", w.Code)
	}
	_, server := testApp(t, nil)
	status, _, _ := request(t, server, "POST", "/api/bench", "", " ")
	if status < 400 {
		t.Fatal("benchmark route accepted a mutation")
	}
}
func TestBenchRejectsMalformed(t *testing.T) {
	data, e := readConsoleAsset("benchmarks/console.json")
	if e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(map[string]any){
		func(d map[string]any) { d["schema_version"] = 2 },
		func(d map[string]any) { d["scenarios"] = []any{} },
		func(d map[string]any) { d["scenarios"].([]any)[0].(map[string]any)["network"] = "WAN" },
		func(d map[string]any) { delete(d["scenarios"].([]any)[0].(map[string]any), "environment") },
		func(d map[string]any) {
			d["scenarios"].([]any)[0].(map[string]any)["summary"].(map[string]any)["ack_p50_ns"] = map[string]any{"median": 0, "min": 0, "max": 0}
		},
		func(d map[string]any) {
			delete(d["scenarios"].([]any)[0].(map[string]any)["summary"].(map[string]any)["ack_p50_ns"].(map[string]any), "median")
		},
	} {
		var d map[string]any
		if e = json.Unmarshal(data, &d); e != nil {
			t.Fatal(e)
		}
		change(d)
		bad, _ := json.Marshal(d)
		if validateBench(bad) == nil {
			t.Fatal("accepted malformed benchmark")
		}
	}
	for _, bad := range []string{"null", "{}", "[1]", "{"} {
		if validateBench([]byte(bad)) == nil {
			t.Fatal("accepted invalid JSON")
		}
	}
}

func TestBenchEmptyStateForMissingOrMalformedAsset(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, malformed := range []bool{false, true} {
		if malformed {
			if e := os.Mkdir("benchmarks", 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile("benchmarks/console.json", []byte(`{"schema_version":1}`), 0600); e != nil {
				t.Fatal(e)
			}
		}
		w := httptest.NewRecorder()
		(&app{}).bench(w, httptest.NewRequest("GET", "/api/bench", nil))
		if w.Code != 503 || !strings.Contains(w.Body.String(), "unavailable or malformed") {
			t.Fatalf("missing empty state: %d", w.Code)
		}
	}
}
