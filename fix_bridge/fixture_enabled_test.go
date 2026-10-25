//go:build quickfix_fixture

// SPDX-License-Identifier: MIT
package main

import (
	"fix_bridge/codec"
	"testing"
)

func TestQuickfixFixtureGuard(t *testing.T) {
	t.Setenv("FXP_FIX_TEST_FIXTURE", "quickfix")
	t.Setenv("FXP_CORE_AUTH_TOKEN", "")
	t.Setenv("FXP_TLS_REQUIRED", "false")
	for _, tc := range []struct {
		flag   bool
		addr   string
		tls    bool
		denied bool
	}{
		{true, "127.0.0.1:0", false, false}, {false, "127.0.0.1:0", false, true},
		{true, "0.0.0.0:0", false, true}, {true, "localhost:0", false, true}, {true, "127.0.0.1:0", true, true},
	} {
		if err := validateTestFixture(tc.flag, tc.addr, tc.tls); (err != nil) != tc.denied {
			t.Fatalf("fixture guard error=%v", err)
		}
	}
	t.Setenv("FXP_CORE_AUTH_TOKEN", "a-core-token-at-least-24-bytes")
	if validateTestFixture(true, "127.0.0.1:0", false) == nil {
		t.Fatal("secure core auth accepted fixture")
	}
}

func TestFixtureOnlyFillsAbsentQuantity(t *testing.T) {
	h := &quickfixFixture{}
	for _, value := range []string{"", "bad", "002000.00"} {
		m := &codec.Message{Fields: map[int]string{35: "D", 38: value}, Tags: []codec.Field{{Tag: 35, Value: "D"}, {Tag: 38, Value: value}}}
		h.PrepareInbound(m)
		if m.Get(38) != value || h.injected != nil {
			t.Fatal("fixture corrected a present invalid/explicit field")
		}
	}
	m := &codec.Message{Fields: map[int]string{35: "D"}, Tags: []codec.Field{{Tag: 35, Value: "D"}}}
	h.PrepareInbound(m)
	if m.Get(38) != "1" || h.injected != m {
		t.Fatal("missing minimum quantity was not supplied")
	}
}
