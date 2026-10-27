// SPDX-License-Identifier: MIT
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"regexp"
	"sort"
	"time"
)

type spread struct {
	Median float64 `json:"median"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

func (s *spread) UnmarshalJSON(data []byte) error {
	var fields struct {
		Median *float64 `json:"median"`
		Min    *float64 `json:"min"`
		Max    *float64 `json:"max"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields.Median == nil || fields.Min == nil || fields.Max == nil {
		return fmt.Errorf("missing spread value")
	}
	s.Median, s.Min, s.Max = *fields.Median, *fields.Min, *fields.Max
	return nil
}

type benchScenario struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	SourceSHA string `json:"source_sha256"`
	GitSHA    string `json:"git_sha"`
	Created   string `json:"created_utc"`
	Network   string `json:"network"`
	Protocol  string `json:"protocol"`
	Config    struct {
		Mode        string `json:"mode"`
		Pacer       string `json:"pacer"`
		Repetitions int    `json:"repetitions"`
		Pinning     string `json:"pinning_notes"`
	} `json:"config"`
	Environment map[string]string `json:"environment"`
	Runs        []json.RawMessage `json:"runs"`
	Summary     map[string]spread `json:"summary"`
}
type benchDocument struct {
	Version   int             `json:"schema_version"`
	Profile   string          `json:"profile"`
	Scenarios []benchScenario `json:"scenarios"`
}

var revisionPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validateBench(data []byte) error {
	var d benchDocument
	if json.Unmarshal(data, &d) != nil || d.Version != 1 || d.Profile != "/api/bench/profile-r4" || len(d.Scenarios) == 0 {
		return fmt.Errorf("invalid benchmark document")
	}
	ids := map[string]bool{}
	for _, s := range d.Scenarios {
		if s.ID == "" || ids[s.ID] || !revisionPattern.MatchString(s.GitSHA) || !digestPattern.MatchString(s.SourceSHA) || s.Source != "benchmarks/round4/"+s.ID+".json.gz" || s.Created == "" || s.Network != "loopback" || s.Protocol != "FXP" || (s.Config.Mode != "open" && s.Config.Mode != "closed") || s.Config.Pacer == "" || s.Config.Pinning == "" || s.Config.Repetitions < 2 || len(s.Runs) != s.Config.Repetitions {
			return fmt.Errorf("invalid scenario")
		}
		ids[s.ID] = true
		for _, k := range []string{"cpu", "os", "arch", "go", "rustc_local", "revision", "pinning_notes"} {
			if s.Environment[k] == "" {
				return fmt.Errorf("missing environment")
			}
		}
		if _, err := time.Parse(time.RFC3339, s.Created); err != nil {
			return fmt.Errorf("invalid creation time")
		}
		if s.Environment["revision"] != s.GitSHA {
			return fmt.Errorf("revision mismatch")
		}
		for _, k := range []string{"ack_p50_ns", "ack_p99_ns", "ack_after_send_p50_ns", "ack_after_send_p99_ns"} {
			v, ok := s.Summary[k]
			if !ok || math.IsNaN(v.Median) || math.IsInf(v.Median, 0) || v.Min < 0 || v.Min > v.Median || v.Median > v.Max {
				return fmt.Errorf("invalid spread")
			}
		}
		for _, endpoint := range []string{"ack", "ack_after_send"} {
			for _, percentile := range []string{"p50_ns", "p99_ns"} {
				samples := []float64{}
				for _, raw := range s.Runs {
					var run map[string]json.RawMessage
					if json.Unmarshal(raw, &run) != nil {
						return fmt.Errorf("invalid run")
					}
					var stat map[string]*float64
					if json.Unmarshal(run[endpoint], &stat) != nil || stat[percentile] == nil || stat["count"] == nil || *stat["count"] <= 0 || *stat[percentile] < 0 {
						return fmt.Errorf("invalid endpoint samples")
					}
					samples = append(samples, *stat[percentile])
				}
				sort.Float64s(samples)
				n := len(samples)
				median := samples[n/2]
				if n%2 == 0 {
					median = (samples[n/2-1] + median) / 2
				}
				v := s.Summary[endpoint+"_"+percentile]
				if v.Min != samples[0] || v.Max != samples[n-1] || v.Median != median {
					return fmt.Errorf("summary disagrees with repetitions")
				}
			}
		}
	}
	return nil
}

// Only fixed, checked-in assets are read; no caller-controlled paths or execution.
func readConsoleAsset(name string) ([]byte, error) {
	data, err := os.ReadFile(name)
	if os.IsNotExist(err) {
		return os.ReadFile("../" + name)
	}
	return data, err
}
func (a *app) bench(w http.ResponseWriter, r *http.Request) {
	data, err := readConsoleAsset("benchmarks/console.json")
	if err != nil || validateBench(data) != nil {
		http.Error(w, "Benchmark data unavailable or malformed; no latency claim can be shown.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}
func (a *app) benchProfile(w http.ResponseWriter, r *http.Request) {
	data, err := readConsoleAsset("docs/performance/profile-r4.md")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}
