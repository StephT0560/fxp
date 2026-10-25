// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const logCapacity = 32 * 1024

type simParams struct {
	Seed         int64 `json:"seed"`
	Symbols      int   `json:"symbols"`
	Participants int   `json:"participants"`
	Duration     int   `json:"duration"`
}

func (p simParams) validate() error {
	if p.Symbols < 1 || p.Symbols > 10 || p.Participants < 1 || p.Participants > 50 || p.Duration < 1 || p.Duration > 300 {
		return fmt.Errorf("symbols must be 1..10, participants 1..50, duration 1..300 seconds")
	}
	return nil
}

type simStatus struct {
	Running   bool      `json:"running"`
	Params    simParams `json:"params"`
	StartedAt time.Time `json:"started_at,omitempty"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	Logs      string    `json:"logs"`
}

// ring is a byte-bounded tail; output is sanitized before it enters the ring.
type logRing struct {
	mu   sync.Mutex
	data []byte
}

func (r *logRing) append(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data = append(r.data, p...)
	if len(r.data) > logCapacity {
		r.data = append([]byte(nil), r.data[len(r.data)-logCapacity:]...)
	}
}
func (r *logRing) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.ToValidUTF8(string(r.data), "�")
}
func (r *logRing) reset() { r.mu.Lock(); defer r.mu.Unlock(); r.data = nil }

type simulator struct {
	mu     sync.Mutex
	cfg    config
	state  simStatus
	logs   logRing
	cancel context.CancelFunc
	done   chan struct{}
	closed bool
}

func (s *simulator) status() simStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.state
	v.Logs = s.logs.text()
	return v
}

// Use an allow-list, not os.Environ(): the child never receives the core token
// or unrelated signing material. JWT claims and shared client TLS settings carry through.
func (s *simulator) childEnv() []string {
	env := []string{"FXP_JWT_SECRET=" + s.cfg.secret, "FXP_SIM_GATEWAY_ADDR=" + s.cfg.simGateway}
	for _, name := range []string{"PATH", "FXP_JWT_ISSUER", "FXP_JWT_AUDIENCE", "FXP_JWT_KID", "FXP_CLIENT_TLS", "FXP_CLIENT_TLS_CA_FILE", "FXP_CLIENT_TLS_CERT_FILE", "FXP_CLIENT_TLS_KEY_FILE", "FXP_CLIENT_TLS_SERVER_NAME"} {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}
func (s *simulator) start(p simParams) error {
	if err := p.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("server is shutting down")
	}
	if s.state.Running {
		return fmt.Errorf("simulator already running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.Duration+30)*time.Second)
	cmd := exec.CommandContext(ctx, s.cfg.simBinary, "-seed", strconv.FormatInt(p.Seed, 10), "-symbols", strconv.Itoa(p.Symbols), "-participants", strconv.Itoa(p.Participants), "-duration", strconv.Itoa(p.Duration))
	cmd.Env = s.childEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Kill the whole process group on timeout/stop, then Wait reaps the child.
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	reader, writer, err := os.Pipe()
	if err != nil {
		cancel()
		return fmt.Errorf("cannot capture simulator output")
	}
	cmd.Stdout = writer
	cmd.Stderr = writer
	s.logs.reset()
	if err := cmd.Start(); err != nil {
		cancel()
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("cannot start configured simulator")
	}
	_ = writer.Close()
	s.cancel = cancel
	s.done = make(chan struct{})
	s.state = simStatus{Running: true, Params: p, StartedAt: time.Now().UTC()}
	done := s.done
	go func() {
		captured := make(chan struct{})
		go func() { defer close(captured); defer reader.Close(); s.capture(reader) }()
		err := cmd.Wait()
		// Close any descendants holding the output pipe even if the leader exited.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cancel()
		<-captured
		code := 0
		if err != nil {
			code = cmd.ProcessState.ExitCode()
		}
		s.mu.Lock()
		s.state.Running = false
		s.state.ExitCode = &code
		s.cancel = nil
		close(done)
		s.mu.Unlock()
	}()
	return nil
}

// Lines are never logged by the BFF. Oversize lines are discarded, and a line
// containing credential material or a JWT is redacted as a whole, including
// secrets split across individual pipe writes.
func (s *simulator) capture(reader io.Reader) {
	buf := make([]byte, 4096)
	line := make([]byte, 0, 4096)
	oversize := false
	flush := func() {
		if oversize {
			s.logs.append([]byte("[oversize log line discarded]\n"))
		} else if strings.Contains(string(line), s.cfg.secret) || strings.Contains(string(line), "eyJ") {
			s.logs.append([]byte("[sensitive log line redacted]\n"))
		} else {
			s.logs.append(line)
		}
		line = line[:0]
		oversize = false
	}
	for {
		n, err := reader.Read(buf)
		for _, b := range buf[:n] {
			if !oversize {
				line = append(line, b)
				if len(line) > 4096 {
					line = line[:0]
					oversize = true
				}
			}
			if b == '\n' {
				flush()
			}
		}
		if err != nil {
			if len(line) > 0 || oversize {
				flush()
			}
			return
		}
	}
}
func (s *simulator) stop(shutdown bool) {
	s.mu.Lock()
	if shutdown {
		s.closed = true
	}
	cancel, done := s.cancel, s.done
	if cancel != nil {
		cancel()
	}
	s.mu.Unlock()
	if cancel != nil {
		<-done
	}
}
