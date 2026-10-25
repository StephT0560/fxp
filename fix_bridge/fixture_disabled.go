//go:build !quickfix_fixture

// SPDX-License-Identifier: MIT
package main

import (
	"errors"
	"fix_bridge/session"
	"net"
	"os"
)

func validateTestFixture(enabled bool, _ string, _ bool) error {
	if enabled || os.Getenv("FXP_FIX_TEST_FIXTURE") != "" {
		return errors.New("test fixture requires a quickfix_fixture test build; unavailable in production binaries")
	}
	return nil
}
func testFixtureHandler(_ net.Conn) session.Handler { return nil }
