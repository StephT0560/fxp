// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"errors"
	"go_gateway/protobuf"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func awaitScenario(t *testing.T, r *scenarioRun) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not finish")
	}
}
func TestScenarioSeedAndFreshSymbols(t *testing.T) {
	m := scenarioManager{execute: func(r *scenarioRun) error {
		return r.step("seeded setup", "ladder", func() (string, error) {
			return "stable outcome", assertScenario(r.qty >= 5 && r.price >= 10000, "bad seeded fixture")
		})
	}}
	a, e := m.start(defaultConfig(), "stop-cascade", 42)
	if e != nil {
		t.Fatal(e)
	}
	awaitScenario(t, a)
	b, e := m.start(defaultConfig(), "stop-cascade", 42)
	if e != nil {
		t.Fatal(e)
	}
	awaitScenario(t, b)
	if a.Symbol == b.Symbol || !strings.HasPrefix(a.Symbol, "SCN-") || a.price != b.price || a.qty != b.qty {
		t.Fatal("seed must repeat workload on different fresh symbols")
	}
	if a.State != "passed" || b.State != "passed" {
		t.Fatal("successful scenario did not pass")
	}
}
func TestDeliberatelyFailingScenarioSurfacesAssertion(t *testing.T) {
	m := scenarioManager{execute: func(r *scenarioRun) error {
		return r.step("check expected fill", "blotter", func() (string, error) { return "", assertScenario(false, "expected quantity %d; got %d", 10, 9) })
	}}
	r, e := m.start(defaultConfig(), "iceberg-priority", 1)
	if e != nil {
		t.Fatal(e)
	}
	awaitScenario(t, r)
	if r.State != "failed" || len(r.events) != 3 || r.events[1].Status != "failed" || !strings.Contains(r.events[2].Message, "check expected fill: expected quantity 10; got 9") {
		t.Fatalf("failure not surfaced precisely: %+v", r.events)
	}
}
func TestScenarioSingleFlightStopAndShutdown(t *testing.T) {
	m := scenarioManager{execute: func(r *scenarioRun) error { <-r.ctx.Done(); return errors.New("connection closed during stop") }}
	r, e := m.start(defaultConfig(), "opening-auction", 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.start(defaultConfig(), "opening-auction", 0); e == nil {
		t.Fatal("concurrent run accepted")
	}
	if e = m.stop("opening-auction", "old-run"); e == nil {
		t.Fatal("stale stop accepted")
	}
	if e = m.stop("opening-auction", r.ID); e != nil {
		t.Fatal(e)
	}
	if r.State != "stopped" {
		t.Fatalf("stop state: %s", r.State)
	}
	m.close()
	if _, e = m.start(defaultConfig(), "opening-auction", 0); e == nil {
		t.Fatal("run after shutdown accepted")
	}
}
func TestScenarioHTTPFailureAndSSEReplay(t *testing.T) {
	a, server := testApp(t, nil)
	a.scenarios.execute = func(r *scenarioRun) error {
		return r.step("self-check", "tape", func() (string, error) { return "", errors.New("expected print 2, observed 1") })
	}
	code, _, _ := request(t, server, "POST", "/api/scenarios/stop-cascade/run", `{"seed":17}`, "")
	if code != 202 {
		t.Fatalf("start %d", code)
	}
	r := a.scenarios.run
	awaitScenario(t, r)
	req, _ := http.NewRequestWithContext(context.Background(), "GET", server.URL+"/api/scenarios/stop-cascade/events?run="+r.ID, nil)
	req.Header.Set("Last-Event-ID", "1")
	res, e := server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = res.Body.Close() }()
	body, e := io.ReadAll(res.Body)
	if e != nil {
		t.Fatal(e)
	}
	if res.Header.Get("Content-Type") != "text/event-stream" || strings.Contains(string(body), "id: 1\n") || !strings.Contains(string(body), "expected print 2, observed 1") || !strings.Contains(string(body), `"kind":"complete"`) {
		t.Fatalf("bad SSE %s", body)
	}
	for _, body := range []string{`{}`, `{"seed":1.2}`, `{"seed":9007199254740992}`, `{"seed":1,"secret":"x"}`} {
		code, _, _ = request(t, server, "POST", "/api/scenarios/stop-cascade/run", body, "")
		if code != 400 {
			t.Fatalf("bad seed accepted: %d", code)
		}
	}
	code, _, _ = request(t, server, "POST", "/api/scenarios/stop-cascade/run", `{"seed":1}`, "http://foreign.invalid")
	if code != 403 {
		t.Fatal("foreign scenario start accepted")
	}
}

func TestScenarioPanicStillSurfacesFailureAndCleanup(t *testing.T) {
	cleaned := false
	m := scenarioManager{execute: func(r *scenarioRun) error {
		r.cleanupExtra = append(r.cleanupExtra, func() error { cleaned = true; return nil })
		panic("test fixture panic")
	}}
	r, e := m.start(defaultConfig(), "opening-auction", 1)
	if e != nil {
		t.Fatal(e)
	}
	awaitScenario(t, r)
	if r.State != "failed" || !cleaned || !strings.Contains(r.events[len(r.events)-1].Message, "orchestrator failure") {
		t.Fatal("panic bypassed cleanup/failure")
	}
}
func TestScenarioUsesCoreExecutionOrder(t *testing.T) {
	reports := []*protobuf.ExecutionReport{{ExecutionId: "X12", OrderId: "second"}, {ExecutionId: "X9", OrderId: "first"}}
	if e := sortScenarioReports(reports); e != nil {
		t.Fatal(e)
	}
	if reports[0].OrderId != "first" {
		t.Fatal("sorted reports by network arrival or lexical ID")
	}
	if sortScenarioReports([]*protobuf.ExecutionReport{{ExecutionId: "unknown"}}) == nil {
		t.Fatal("invented execution ordering")
	}
}

func TestSimulatorAndScenarioCannotOverlap(t *testing.T) {
	a, server := testApp(t, nil)
	a.sim.mu.Lock()
	a.sim.state.Running = true
	a.sim.mu.Unlock()
	code, _, _ := request(t, server, "POST", "/api/scenarios/opening-auction/run", `{"seed":1}`, "")
	a.sim.mu.Lock()
	a.sim.state.Running = false
	a.sim.mu.Unlock()
	if code != 409 {
		t.Fatalf("scenario started over simulator: %d", code)
	}
	a.scenarios.execute = func(r *scenarioRun) error { <-r.ctx.Done(); return r.ctx.Err() }
	code, _, _ = request(t, server, "POST", "/api/scenarios/opening-auction/run", `{"seed":1}`, "")
	if code != 202 {
		t.Fatal("could not start scenario")
	}
	code, _, _ = request(t, server, "POST", "/api/sim/start", `{"seed":1,"symbols":1,"participants":2,"duration":30}`, "")
	if code != 409 {
		t.Fatalf("simulator started over scenario: %d", code)
	}
}
