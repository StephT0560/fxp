// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go_gateway/protobuf"
)

// fakeGateway accepts one connection, checks the operator Logon and answers
// the SessionControl with reject (empty = accepted).
func fakeGateway(t *testing.T, reject string) (string, <-chan *protobuf.SessionControl) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan *protobuf.SessionControl, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		logon, err := readFrame(conn)
		if err != nil || logon.GetLogon() == nil {
			return
		}
		raw, _ := base64.RawURLEncoding.DecodeString(strings.Split(logon.GetLogon().Token, ".")[1])
		var claims map[string]any
		_ = json.Unmarshal(raw, &claims)
		accepted := claims["fxp_role"] == "operator" && claims["sub"] == operatorSubject
		_ = writeFrame(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_LogonAck{LogonAck: &protobuf.LogonAck{Accepted: accepted}}})
		msg, err := readFrame(conn)
		if err != nil || msg.GetSessionControl() == nil {
			return
		}
		got <- msg.GetSessionControl()
		id := msg.GetSessionControl().RequestId
		_ = writeFrame(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_SessionControlReport{SessionControlReport: &protobuf.SessionControlReport{RequestId: "other"}}})
		_ = writeFrame(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_SessionControlReport{SessionControlReport: &protobuf.SessionControlReport{RequestId: id, SymbolsAffected: 10, RejectReason: reject}}})
	}()
	return ln.Addr().String(), got
}

func TestOpenCallSendsOperatorSessionControl(t *testing.T) {
	c := defaultConfig()
	c.secret = strings.Repeat("s", 32)
	addr, got := fakeGateway(t, "")
	c.simGateway = addr
	if err := c.openCall(context.Background(), 20); err != nil {
		t.Fatal(err)
	}
	if sc := <-got; sc.OpeningCallSeconds != 20 {
		t.Fatalf("sent %+v", sc)
	}
	addr, _ = fakeGateway(t, "operator role required")
	c.simGateway = addr
	if err := c.openCall(context.Background(), 20); err == nil || !strings.Contains(err.Error(), "operator role required") {
		t.Fatalf("reject not surfaced: %v", err)
	}
}

func TestSimStartOpeningAuction(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	binary := filepath.Join(dir, "simulator")
	released := filepath.Join(dir, "released")
	script := "#!/bin/sh\necho \"$@\" > " + argsFile + "\necho 'All participants connected'\nread line\necho \"$line\" > " + released + "\nsleep 30\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a, server := testApp(t, func(c *config) { c.simBinary = binary })
	origin := a.cfg.origin
	var calls []int
	fail := errors.New("gateway unreachable")
	a.openCall = func(_ context.Context, seconds int) error {
		calls = append(calls, seconds)
		return fail
	}
	body := `{"seed":7,"symbols":2,"participants":4,"duration":60,"opening_auction_sec":20}`
	if code, data, _ := request(t, server, http.MethodPost, "/api/sim/start", body, origin); code != http.StatusBadGateway || !strings.Contains(string(data), "gateway unreachable") {
		t.Fatalf("open-call failure: %d %s", code, data)
	}
	if a.sim.status().Running {
		t.Fatal("simulator kept running without the opening call")
	}
	if _, err := os.Stat(released); err == nil {
		t.Fatal("simulator released without the opening call")
	}
	for _, bad := range []string{`"opening_auction_sec":3`, `"opening_auction_sec":60`, `"opening_auction_sec":121`} {
		b := `{"seed":7,"symbols":2,"participants":4,"duration":60,` + bad + `}`
		if code, _, _ := request(t, server, http.MethodPost, "/api/sim/start", b, origin); code != http.StatusBadRequest {
			t.Fatalf("%s accepted: %d", bad, code)
		}
	}
	fail = nil
	if code, data, _ := request(t, server, http.MethodPost, "/api/sim/start", body, origin); code != http.StatusAccepted {
		t.Fatalf("start: %d %s", code, data)
	}
	if len(calls) != 2 || calls[1] != 20 {
		t.Fatalf("open calls %v", calls)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		args, _ := os.ReadFile(argsFile)
		release, _ := os.ReadFile(released)
		if strings.TrimSpace(string(release)) == "go" && strings.Contains(string(args), "-opening-auction-sec 20") && strings.Contains(string(args), "-seed 7") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("simulator args %q, release signal %q", args, release)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, _, _ := request(t, server, http.MethodPost, "/api/sim/start", body, origin); code != http.StatusConflict {
		t.Fatalf("second start: %d", code)
	}
	if len(calls) != 2 {
		t.Fatal("opening call sent while the simulator runs")
	}
}
