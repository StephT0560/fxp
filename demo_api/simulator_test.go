// SPDX-License-Identifier: MIT
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogRingBoundsAndRedaction(t *testing.T) {
	s := simulator{cfg: config{secret: "sensitive-test-secret"}}
	input := "safe\nsecret=sensitive-test-secret\neyJhbGciOiJIUzI1NiJ9.fake.signature\n" + strings.Repeat("x", 5000) + "\n"
	s.capture(bytes.NewBufferString(input))
	got := s.logs.text()
	if strings.Contains(got, s.cfg.secret) || strings.Contains(got, "eyJ") || !strings.Contains(got, "safe") || !strings.Contains(got, "discarded") {
		t.Fatal("sensitive output leaked")
	}
	for range 100 {
		s.logs.append(bytes.Repeat([]byte("a"), 1000))
	}
	if len(s.logs.text()) > logCapacity {
		t.Fatal("log ring unbounded")
	}
}
func TestSimulatorSupervisionAndEnvironment(t *testing.T) {
	t.Setenv("FXP_CORE_AUTH_TOKEN", "must-not-reach-child")
	dir := t.TempDir()
	binary := filepath.Join(dir, "simulator")
	fixture := "#!/bin/sh\nprintf 'safe child output\\n'\nprintf '%s\\n' \"$FXP_JWT_SECRET\"\nif [ -n \"${FXP_CORE_AUTH_TOKEN:-}\" ]; then printf 'core leaked\\n'; fi\nsleep 30\n"
	if err := os.WriteFile(binary, []byte(fixture), 0700); err != nil {
		t.Fatal(err)
	}
	c := defaultConfig()
	c.secret = strings.Repeat("s", 32)
	c.simBinary = binary
	s := &simulator{cfg: c}
	t.Cleanup(func() { s.stop(true) })
	p := simParams{Seed: 7, Symbols: 2, Participants: 4, Duration: 5}
	if err := s.start(p); err != nil {
		t.Fatal(err)
	}
	if s.start(p) == nil {
		t.Fatal("second simulator accepted")
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(s.status().Logs, "safe child") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	status := s.status()
	if !status.Running || strings.Contains(status.Logs, c.secret) || strings.Contains(status.Logs, "core leaked") {
		t.Fatal("invalid child state/log redaction")
	}
	s.stop(false)
	if s.status().Running || s.status().ExitCode == nil {
		t.Fatal("child not reaped")
	}
	if err := s.start(p); err != nil {
		t.Fatal("could not restart")
	}
	s.stop(true)
	if s.start(p) == nil {
		t.Fatal("started after shutdown")
	}
}

func TestSimulatorHardTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("31-second supervision deadline")
	}
	binary := filepath.Join(t.TempDir(), "simulator")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nsleep 120\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c := defaultConfig()
	c.secret = strings.Repeat("s", 32)
	c.simBinary = binary
	s := &simulator{cfg: c}
	t.Cleanup(func() { s.stop(true) })
	if err := s.start(simParams{Seed: 7, Symbols: 1, Participants: 1, Duration: 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.done:
		if s.status().Running {
			t.Fatal("deadline left child running")
		}
	case <-time.After(35 * time.Second):
		t.Fatal("hard deadline not enforced")
	}
}
