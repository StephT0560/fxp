// SPDX-License-Identifier: MIT
package main

import (
	"flag"
	"io"
	"strings"
	"testing"

	"go_gateway/startup"
)

func TestConnectionConfig(t *testing.T) {
	t.Setenv("FXP_JWT_SECRET", strings.Repeat("s", startup.MinSecretBytes))
	for _, name := range []string{"FXP_CLIENT_TLS", "FXP_CLIENT_TLS_CA_FILE", "FXP_CLIENT_TLS_CERT_FILE", "FXP_CLIENT_TLS_KEY_FILE", "FXP_CLIENT_TLS_SERVER_NAME"} {
		t.Setenv(name, "")
	}
	for _, tc := range []struct {
		name, env string
		args      []string
		want      string
		tls       bool
	}{
		{name: "default", want: "127.0.0.1:8082"},
		{name: "environment", env: "localhost:9082", want: "localhost:9082"},
		{name: "flag overrides environment", env: "localhost:9082", args: []string{"-gateway", "localhost:10082", "-tls=false"}, want: "localhost:10082"},
		{name: "TLS flags", args: []string{"-tls", "-tls-server-name", "localhost"}, want: "127.0.0.1:8082", tls: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FXP_SIM_GATEWAY_ADDR", tc.env)
			t.Setenv("FXP_CLIENT_TLS", "true")
			if tc.name != "flag overrides environment" {
				t.Setenv("FXP_CLIENT_TLS", "false")
			}
			f := flag.NewFlagSet("simday", flag.ContinueOnError)
			f.SetOutput(io.Discard)
			got, err := parseConnectionConfig(f, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if got.address != tc.want || got.options.Enabled != tc.tls || got.secret != strings.Repeat("s", startup.MinSecretBytes) {
				t.Fatal("unexpected connection configuration")
			}
			if tc.tls && got.options.ServerName != "localhost" {
				t.Fatal("TLS server name lost")
			}
		})
	}
}

func TestSecretValidation(t *testing.T) {
	for _, size := range []int{0, startup.MinSecretBytes - 1, startup.MinSecretBytes, 32} {
		secret := strings.Repeat("x", size)
		t.Setenv("FXP_JWT_SECRET", secret)
		err := validateSecret(secret)
		if (err != nil) != (size < startup.MinSecretBytes) {
			t.Fatalf("size %d: %v", size, err)
		}
		if err != nil && (!strings.Contains(err.Error(), "FXP_JWT_SECRET") || (secret != "" && strings.Contains(err.Error(), secret))) {
			t.Fatal("error must identify variable without exposing value")
		}
	}
}

func TestSecretFlagForbidden(t *testing.T) {
	t.Setenv("FXP_JWT_SECRET", strings.Repeat("x", 32))
	t.Setenv("FXP_CLIENT_TLS", "false")
	f := flag.NewFlagSet("simday", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	if _, err := parseConnectionConfig(f, []string{"-jwt-secret", "forbidden"}); err == nil {
		t.Fatal("secret flag accepted")
	}
}

func TestTLSConfigurationValidation(t *testing.T) {
	t.Setenv("FXP_JWT_SECRET", strings.Repeat("s", 32))
	for _, name := range []string{"FXP_CLIENT_TLS_CA_FILE", "FXP_CLIENT_TLS_CERT_FILE", "FXP_CLIENT_TLS_KEY_FILE", "FXP_CLIENT_TLS_SERVER_NAME"} {
		t.Setenv(name, "")
	}
	for _, tc := range []struct {
		name, enabled, server string
		args                  []string
		wantError             bool
	}{
		{name: "TLS environment defaults", enabled: "true", server: "env.example"},
		{name: "TLS flag overrides environment", enabled: "true", server: "env.example", args: []string{"-tls-server-name", "flag.example"}},
		{name: "invalid boolean", enabled: "invalid", wantError: true},
		{name: "TLS settings without TLS", enabled: "false", server: "env.example", wantError: true},
		{name: "unpaired client certificate", enabled: "true", args: []string{"-tls-cert", "missing.pem"}, wantError: true},
		{name: "missing CA file", enabled: "true", args: []string{"-tls-ca", "missing.pem"}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FXP_CLIENT_TLS", tc.enabled)
			t.Setenv("FXP_CLIENT_TLS_SERVER_NAME", tc.server)
			f := flag.NewFlagSet("simday", flag.ContinueOnError)
			f.SetOutput(io.Discard)
			got, err := parseConnectionConfig(f, tc.args)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v", err)
			}
			if err == nil {
				want := tc.server
				if len(tc.args) > 0 {
					want = "flag.example"
				}
				if got.options.ServerName != want || !got.options.Enabled {
					t.Fatal("TLS defaults or override lost")
				}
			}
		})
	}
}
