// SPDX-License-Identifier: MIT

package logging

import (
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
		ok   bool
	}{
		{"", slog.LevelInfo, true},
		{"debug", slog.LevelDebug, true},
		{"DEBUG", slog.LevelDebug, true},
		{"info", slog.LevelInfo, true},
		{"warn", slog.LevelWarn, true},
		{"error", slog.LevelError, true},
		{"verbose", slog.LevelInfo, false},
	}
	for _, c := range cases {
		got, ok := ParseLevel(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestSetupFiltersDebugByDefault(t *testing.T) {
	t.Setenv(EnvVar, "")
	Setup()
	if slog.Default().Enabled(t.Context(), slog.LevelDebug) {
		t.Fatal("debug logging enabled with empty " + EnvVar)
	}
	t.Setenv(EnvVar, "debug")
	Setup()
	if !slog.Default().Enabled(t.Context(), slog.LevelDebug) {
		t.Fatal("debug logging disabled with " + EnvVar + "=debug")
	}
}
