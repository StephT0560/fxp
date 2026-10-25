// SPDX-License-Identifier: MIT
package main

import (
	"strings"
	"testing"
	"time"
)

func TestSecretRefusal(t *testing.T) {
	for _, secret := range []string{"", strings.Repeat("s", 23), "dev-secret-change-in-production"} {
		c := defaultConfig()
		c.secret = secret
		if c.validate() == nil {
			t.Fatal("unsafe secret accepted")
		}
	}
	c := defaultConfig()
	c.secret = strings.Repeat("s", 24)
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
}
func TestListenerAndLifetime(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:3000", "[::]:3000", "localhost:3000", "example.com:3000"} {
		c := defaultConfig()
		c.secret = strings.Repeat("s", 32)
		c.addr = addr
		if c.validate() == nil {
			t.Fatalf("external/ambiguous address accepted: %s", addr)
		}
		c.allowPlaintext = true
		if err := c.validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, ttl := range []time.Duration{0, time.Second * 901, -time.Second} {
		c := defaultConfig()
		c.secret = strings.Repeat("s", 32)
		c.tokenTTL = ttl
		if c.validate() == nil {
			t.Fatal("unsafe token TTL accepted")
		}
	}
}
func TestSimulationBounds(t *testing.T) {
	valid := simParams{Seed: 7, Symbols: 2, Participants: 4, Duration: 5}
	if valid.validate() != nil {
		t.Fatal("valid request refused")
	}
	for _, p := range []simParams{{}, {Seed: 7, Symbols: 11, Participants: 4, Duration: 5}, {Seed: 7, Symbols: 2, Participants: 51, Duration: 5}, {Seed: 7, Symbols: 2, Participants: 4, Duration: 301}, {Seed: 7, Symbols: -1, Participants: 4, Duration: 5}} {
		if p.validate() == nil {
			t.Fatal("unbounded request accepted")
		}
	}
}
