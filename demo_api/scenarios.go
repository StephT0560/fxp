// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	mathrand "math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type scenarioInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

var scenarioCatalog = []scenarioInfo{
	{"opening-auction", "Opening auction uncross", "Crossed at-open orders wait, then uncross into one public print."},
	{"stop-cascade", "Stop cascade", "Successive public prints trigger two stops in price order."},
	{"stp-modes", "STP modes side by side", "The same setup checks NONE, cancel newest, cancel oldest, cancel both, and decrement-and-cancel."},
	{"iceberg-priority", "Iceberg replenish loses priority", "A replenished slice queues behind an already resting peer."},
	{"cancel-on-disconnect", "Cancel on disconnect", "Drop a TCP session, verify an empty book and stored canceled reports."},
	{"fix-recovery", "FIX restart and sequence recovery", "Kill a private bridge, restart its FileStore, and resend the original execution report."},
}

type scenarioEvent struct {
	Seq       int             `json:"seq"`
	Kind      string          `json:"kind"`
	Step      string          `json:"step,omitempty"`
	Panel     string          `json:"panel,omitempty"`
	Status    string          `json:"status,omitempty"`
	Message   string          `json:"message,omitempty"`
	Outcome   string          `json:"outcome,omitempty"`
	ElapsedMS int64           `json:"elapsed_ms"`
	Frame     json.RawMessage `json:"frame,omitempty"`
	Actor     string          `json:"actor,omitempty"`
}
type scenarioRun struct {
	narrationDelay time.Duration
	overflow       bool
	ID             string `json:"run_id"`
	Scenario       string `json:"scenario"`
	Symbol         string `json:"symbol"`
	Seed           int64  `json:"seed"`
	State          string `json:"state"`
	mu             sync.Mutex
	events         []scenarioEvent
	wake           chan struct{}
	done           chan struct{}
	cancel         context.CancelFunc
	started        time.Time
	cfg            config
	ctx            context.Context
	sessions       []*scenarioSession
	ownerPrefix    string
	price          int64
	qty            int32
	cleanupExtra   []func() error
	externalOwners []string
}
type scenarioManager struct {
	mu      sync.Mutex
	run     *scenarioRun
	closed  bool
	execute func(*scenarioRun) error
}

func knownScenario(id string) bool {
	for _, v := range scenarioCatalog {
		if v.ID == id {
			return true
		}
	}
	return false
}
func (m *scenarioManager) start(cfg config, id string, seed int64) (*scenarioRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("scenario runner is shutting down")
	}
	if !knownScenario(id) {
		return nil, errors.New("unknown scenario")
	}
	if m.run != nil {
		select {
		case <-m.run.done:
		default:
			return nil, errors.New("another scenario is running; stop it first")
		}
	}
	var nonce [8]byte
	if _, e := rand.Read(nonce[:]); e != nil {
		return nil, errors.New("cannot allocate fresh scenario")
	}
	token := hex.EncodeToString(nonce[:])
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	rng := mathrand.New(mathrand.NewSource(seed))
	r := &scenarioRun{ID: token, Scenario: id, Symbol: "SCN-" + token, Seed: seed, State: "running", wake: make(chan struct{}), done: make(chan struct{}), started: time.Now(), cancel: cancel, cfg: cfg, ctx: ctx, ownerPrefix: "demo:scenario:" + token + ":", price: 10000 + int64(rng.Intn(100))*100, qty: int32(5 + rng.Intn(6))}
	if m.execute == nil {
		r.narrationDelay = 600 * time.Millisecond
	}
	m.run = r
	go func() {
		execute := m.execute
		if execute == nil {
			execute = executeScenario
		}
		err := executeScenarioSafely(r, execute)
		// TCP closure may surface as EOF before a wait observes ctx.Done().
		if errors.Is(r.ctx.Err(), context.Canceled) {
			err = context.Canceled
		}
		// Cleanup has an independent deadline and completes before the terminal event.
		cleanupErr := r.cleanup()
		state := "passed"
		message := "All assertions passed; scenario orders and child processes cleaned up."
		if err != nil {
			state = "failed"
			message = err.Error()
			if errors.Is(err, context.Canceled) {
				state = "stopped"
				message = "Stopped by viewer; cleanup completed."
			}
		}
		r.mu.Lock()
		overflow := r.overflow
		r.mu.Unlock()
		if overflow {
			state = "failed"
			message = "Scenario observation limit exceeded; run canceled and cleaned up."
		}
		if cleanupErr != nil {
			state = "failed"
			message += " Cleanup failed: " + cleanupErr.Error()
		}
		r.mu.Lock()
		r.State = state
		r.mu.Unlock()
		r.emit(scenarioEvent{Kind: "complete", Status: state, Message: message, Outcome: state})
		cancel()
		close(r.done)
	}()
	return r, nil
}
func (r *scenarioRun) emit(e scenarioEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.Kind == "frame" && len(r.events) >= 2000 {
		r.overflow = true
		r.cancel()
		return
	}
	e.Seq = len(r.events) + 1
	e.ElapsedMS = time.Since(r.started).Milliseconds()
	r.events = append(r.events, e)
	close(r.wake)
	r.wake = make(chan struct{})
}
func (r *scenarioRun) view() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return map[string]any{"run_id": r.ID, "scenario": r.Scenario, "symbol": r.Symbol, "seed": r.Seed, "state": r.State}
}
func (r *scenarioRun) step(name, panel string, fn func() (string, error)) error {
	r.emit(scenarioEvent{Kind: "step", Step: name, Panel: panel, Status: "running", Message: name})
	outcome, err := fn()
	status := "passed"
	message := outcome
	if err != nil {
		status = "failed"
		message = err.Error()
	}
	r.emit(scenarioEvent{Kind: "step", Step: name, Panel: panel, Status: status, Message: message, Outcome: outcome})
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if r.narrationDelay > 0 {
		timer := time.NewTimer(r.narrationDelay)
		defer timer.Stop()
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}
func executeScenarioSafely(r *scenarioRun, execute func(*scenarioRun) error) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("unexpected orchestrator failure; cleanup required")
		}
	}()
	return execute(r)
}
func assertScenario(ok bool, format string, args ...any) error {
	if !ok {
		return fmt.Errorf(format, args...)
	}
	return nil
}
func (m *scenarioManager) stop(id, runID string) error {
	m.mu.Lock()
	r := m.run
	m.mu.Unlock()
	if r == nil || r.Scenario != id || (runID != "" && r.ID != runID) {
		return errors.New("scenario run not found")
	}
	r.cancel()
	select {
	case <-r.done:
		return nil
	case <-time.After(15 * time.Second):
		return errors.New("scenario cleanup timed out")
	}
}
func (m *scenarioManager) close() {
	m.mu.Lock()
	m.closed = true
	r := m.run
	m.mu.Unlock()
	if r != nil {
		r.cancel()
		<-r.done
	}
}
func (a *app) scenariosList(w http.ResponseWriter, r *http.Request) {
	a.scenarios.mu.Lock()
	run := a.scenarios.run
	a.scenarios.mu.Unlock()
	var view any
	if run != nil {
		view = run.view()
	}
	writeJSON(w, http.StatusOK, map[string]any{"scenarios": scenarioCatalog, "run": view})
}
func (a *app) scenarioStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !knownScenario(id) {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Seed *int64 `json:"seed"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Seed == nil || *req.Seed > 1<<53-1 || *req.Seed < -(1<<53-1) {
		http.Error(w, "seed must be a safe integer", http.StatusBadRequest)
		return
	}
	a.activityMu.Lock()
	defer a.activityMu.Unlock()
	if a.sim.status().Running {
		http.Error(w, "stop the simulator before running a scenario", http.StatusConflict)
		return
	}
	run, err := a.scenarios.start(a.cfg, id, *req.Seed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusAccepted, run.view())
}
func (a *app) scenarioStop(w http.ResponseWriter, r *http.Request) {
	a.scenarios.mu.Lock()
	run := a.scenarios.run
	a.scenarios.mu.Unlock()
	if run == nil || run.Scenario != r.PathValue("id") || (r.URL.Query().Get("run") != "" && r.URL.Query().Get("run") != run.ID) {
		http.Error(w, "scenario run not found", http.StatusConflict)
		return
	}
	if err := a.scenarios.stop(run.Scenario, run.ID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, run.view())
}

func (a *app) scenarioEvents(w http.ResponseWriter, r *http.Request) {
	a.scenarios.mu.Lock()
	run := a.scenarios.run
	a.scenarios.mu.Unlock()
	if run == nil || run.ID != r.URL.Query().Get("run") || run.Scenario != r.PathValue("id") {
		http.NotFound(w, r)
		return
	}
	cursor := 0
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 0 || n > math.MaxInt32 {
			http.Error(w, "invalid event cursor", http.StatusBadRequest)
			return
		}
		cursor = n
	}
	run.mu.Lock()
	length := len(run.events)
	complete := false
	select {
	case <-run.done:
		complete = true
	default:
	}
	run.mu.Unlock()
	if cursor > length {
		http.Error(w, "event cursor ahead of run", http.StatusBadRequest)
		return
	}
	if complete && cursor == length {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	// Bound each write without imposing a deadline on the long-lived stream.
	rc := http.NewResponseController(w)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		run.mu.Lock()
		events := append([]scenarioEvent(nil), run.events...)
		wake := run.wake
		run.mu.Unlock()
		if cursor > len(events) {
			http.Error(w, "event cursor ahead of run", http.StatusBadRequest)
			return
		}
		for _, e := range events[cursor:] {
			data, _ := json.Marshal(e)
			_ = rc.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.Seq, data); err != nil {
				return
			}
			cursor = e.Seq
			f.Flush()
			if e.Kind == "complete" {
				return
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-wake:
		case <-ticker.C:
			_ = rc.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			f.Flush()
		}
	}
}
