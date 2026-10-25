//go:build !quickfix_fixture

// SPDX-License-Identifier: MIT
package main

import "testing"

func TestProductionBuildCannotEnableFixture(t *testing.T) {
	t.Setenv("FXP_FIX_TEST_FIXTURE", "quickfix")
	if validateTestFixture(false, "127.0.0.1:8083", false) == nil {
		t.Fatal("production build accepted fixture environment")
	}
	if validateTestFixture(true, "127.0.0.1:8083", false) == nil {
		t.Fatal("production build accepted fixture flag")
	}
}
