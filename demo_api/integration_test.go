//go:build integration

// SPDX-License-Identifier: MIT
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
)

type safeBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}
func (b *safeBuffer) contents() string { b.mu.Lock(); defer b.mu.Unlock(); return b.data.String() }
func TestIntegrationRealStack(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	secret := hex.EncodeToString(raw)
	// Fixed core/gateway ports are a repository constraint; the shell runner checks
	// for collisions before building. BFF itself uses a free loopback port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	base := "http://" + addr
	env := []string{"PATH=" + os.Getenv("PATH"), "FXP_JWT_SECRET=" + secret, "FXP_JWT_ISSUER=bff-integration", "FXP_JWT_AUDIENCE=bff-gateway", "FXP_JWT_KID=bff-key", "FXP_GW_TCP_ADDR=127.0.0.1:8082", "FXP_GW_WS_ADDR=127.0.0.1:8081", "FXP_DEMO_ADDR=" + addr, "FXP_DEMO_ORIGIN=" + base, "FXP_DEMO_SIM_BINARY=" + filepath.Join(root, "bin/simday"), "FXP_SYMBOL_CONFIG=" + filepath.Join(root, "config/symbols.demo.csv")}
	type process struct {
		cmd  *exec.Cmd
		done chan error
		logs *safeBuffer
	}
	start := func(binary string) *process {
		p := &process{cmd: exec.Command(binary), done: make(chan error, 1), logs: new(safeBuffer)}
		p.cmd.Env = env
		p.cmd.Stdout = p.logs
		p.cmd.Stderr = p.logs
		p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := p.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { p.done <- p.cmd.Wait() }()
		t.Cleanup(func() { _ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL) })
		return p
	}
	waitPort := func(port string) {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			c, e := net.DialTimeout("tcp", port, 100*time.Millisecond)
			if e == nil {
				_ = c.Close()
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("listener %s not ready", port)
	}
	core := start(filepath.Join(root, "fxp_core/target/release/fxp_core"))
	waitPort("127.0.0.1:8080")
	gateway := start(filepath.Join(root, "bin/go_gateway"))
	waitPort("127.0.0.1:8082")
	bff := start(filepath.Join(root, "bin/demo_api"))
	waitPort(addr)
	client := &http.Client{Timeout: 5 * time.Second}
	call := func(method, path, body string) (int, []byte) {
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", base)
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("secret in API response")
		}
		return res.StatusCode, data
	}
	for _, path := range []string{"/healthz", "/api/users", "/api/symbols", "/api/sim/status", "/"} {
		if code, _ := call("GET", path, ""); code != 200 {
			t.Fatalf("%s: %d", path, code)
		}
	}
	code, body := call("POST", "/api/token", `{"user":"alice"}`)
	if code != 200 {
		t.Fatal("token rejected")
	}
	var token struct {
		Token string `json:"token"`
	}
	if err = json.Unmarshal(body, &token); err != nil {
		t.Fatal(err)
	}
	wsURL := "ws" + strings.TrimPrefix(base, "http") + "/ws"
	conn, res, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {"http://foreign.example"}})
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || res.StatusCode != 403 {
		t.Fatal("foreign Origin accepted")
	}
	_ = res.Body.Close()
	conn, _, err = websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {base}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send := func(msg *protobuf.FXPMessage) {
		data, err := proto.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		if err = conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
			t.Fatal(err)
		}
	}
	receive := func() *protobuf.FXPMessage {
		_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		kind, data, err := conn.ReadMessage()
		if err != nil || kind != websocket.BinaryMessage {
			_, state := call("GET", "/api/sim/status", "")
			t.Log(string(state))
			t.Log(gateway.logs.contents())
			t.Fatalf("binary response missing: %v", err)
		}
		msg := new(protobuf.FXPMessage)
		if err = proto.Unmarshal(data, msg); err != nil {
			t.Fatal(err)
		}
		return msg
	}
	send(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_Logon{Logon: &protobuf.Logon{Token: token.Token, SessionId: "bff-integration"}}})
	if ack := receive().GetLogonAck(); ack == nil || !ack.Accepted {
		t.Fatal("gateway rejected BFF JWT through proxy")
	}
	// The core only subscribes existing books. Admit both symbols with passive
	// orders first; changing that core contract is outside this BFF work item.
	for i, symbol := range []string{"AAPL", "MSFT"} {
		id := fmt.Sprintf("bff-seed-%d", i)
		send(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_NewOrder{NewOrder: &protobuf.NewOrderSingle{Header: &protobuf.FXPHeader{ProtocolVersion: "2.5"}, OrderId: id, Sender: "demo:alice", Symbol: symbol, Side: protobuf.OrderSide_BUY, Price: 100, Quantity: 1, OrderType: protobuf.OrderType_LIMIT, TimeInForce: protobuf.TimeInForce_GTC}}})
		report := receive().GetExecutionReport()
		if report == nil || report.ExecutionType != protobuf.ExecutionType_EXEC_NEW {
			t.Fatal("could not admit demo symbol")
		}
	}
	send(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_MarketDataRequest{MarketDataRequest: &protobuf.MarketDataRequest{Header: &protobuf.FXPHeader{ProtocolVersion: "2.5"}, Symbols: []string{"AAPL", "MSFT"}}}})
	params := `{"seed":7,"symbols":2,"participants":4,"duration":5}`
	if code, _ := call("POST", "/api/sim/start", params); code != 202 {
		t.Fatal("simulator did not start")
	}
	if code, _ := call("POST", "/api/sim/start", params); code != 409 {
		t.Fatal("parallel simulator accepted")
	}
	var sim simStatus
	_, body = call("GET", "/api/sim/status", "")
	if err = json.Unmarshal(body, &sim); err != nil || !sim.Running {
		t.Fatal("simulator not running")
	}
	// The public trade payload is produced by the REAL independent Rust engine,
	// decoded with protobuf-go after crossing the byte-transparent WS proxy.
	for {
		trade := receive().GetTradeCaptureReport()
		if trade == nil {
			continue
		}
		if trade.TradeQuantity <= 0 || trade.Seq == 0 || trade.BuyOrderId != "" || trade.SellOrderId != "" {
			t.Fatal("invalid anonymous Rust trade")
		}
		break
	}
	// Confirm environment-only credentials for BFF, core, gateway and simulator.
	for _, p := range []*process{core, gateway, bff} {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", p.cmd.Process.Pid))
		if err != nil || bytes.Contains(data, []byte(secret)) {
			t.Fatal("unsafe/missing process command line")
		}
	}
	children, err := childProcesses(bff.cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Fields(string(children))) == 0 {
		t.Fatal("simulator child not found")
	}
	for _, pid := range strings.Fields(string(children)) {
		data, err := os.ReadFile("/proc/" + pid + "/cmdline")
		if err != nil || bytes.Contains(data, []byte(secret)) {
			t.Fatal("secret in simulator argv")
		}
	}
	if code, _ := call("POST", "/api/sim/stop", `{}`); code != 200 {
		t.Fatal("stop failed")
	}
	_, body = call("GET", "/api/sim/status", "")
	if err = json.Unmarshal(body, &sim); err != nil || sim.Running || sim.ExitCode == nil {
		t.Fatal("simulator not reaped")
	}
	if code, _ := call("POST", "/api/sim/start", params); code != 202 {
		t.Fatal("restart failed")
	}
	children, _ = childProcesses(bff.cmd.Process.Pid)
	if err := bff.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-bff.done:
		if err != nil {
			t.Fatal("BFF shutdown failed")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("shutdown timed out")
	}
	for _, pid := range strings.Fields(string(children)) {
		if _, err := strconv.Atoi(pid); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat("/proc/" + pid); err == nil {
			t.Fatal("child survived shutdown")
		}
	}
	for _, p := range []*process{core, gateway, bff} {
		logs := p.logs.contents()
		if strings.Contains(logs, secret) || strings.Contains(logs, token.Token) {
			t.Fatal("credential in process logs")
		}
	}
	t.Log("PASS token -> same-origin WS -> LogonAck; foreign Origin 403; Rust public trades; sim start/status/stop/shutdown; credentials absent from responses/logs/argv")
}

func childProcesses(pid int) ([]byte, error) {
	output, err := exec.Command("ps", "-eo", "pid=,ppid=").Output()
	if err != nil {
		return nil, err
	}
	var children []byte
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == strconv.Itoa(pid) {
			children = append(children, fields[0]...)
			children = append(children, ' ')
		}
	}
	return children, nil
}
