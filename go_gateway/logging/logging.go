// SPDX-License-Identifier: MIT

// Package logging configures the process-wide slog logger for the gateway and
// the FIX bridge. Per-message events are logged at DEBUG; everything else at
// INFO and above. Legacy log.Printf calls are routed through the same handler.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// EnvVar selects the minimum log level: debug, info (default), warn or error.
const EnvVar = "FXP_LOG_LEVEL"

// ParseLevel maps an FXP_LOG_LEVEL value to a slog level. Empty means info;
// unknown values return info and ok=false.
func ParseLevel(s string) (slog.Level, bool) {
	if strings.TrimSpace(s) == "" {
		return slog.LevelInfo, true
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(strings.TrimSpace(s))); err != nil {
		return slog.LevelInfo, false
	}
	return lvl, true
}

// Setup installs a text handler on stderr at the level given by FXP_LOG_LEVEL.
func Setup() {
	raw := os.Getenv(EnvVar)
	lvl, ok := ParseLevel(raw)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))
	if !ok {
		slog.Warn("ignoring invalid log level", "env", EnvVar, "value", raw, "using", lvl.String())
	}
}
