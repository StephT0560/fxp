// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"fix_bridge/codec"
	"go_gateway/protobuf"
	"go_gateway/test_clients/devjwt"
)

type scenarioBridge struct {
	cmd  *exec.Cmd
	done chan struct{}
	once sync.Once
}

func (p *scenarioBridge) kill() error {
	p.once.Do(func() { _ = p.cmd.Process.Kill() })
	select {
	case <-p.done:
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("owned FIX bridge did not exit")
	}
}
func (r *scenarioRun) launchBridge(ctx context.Context, dir, comp, password string) (*scenarioBridge, string, error) {
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return nil, "", errors.New("cannot reserve ephemeral FIX port")
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	cmd := exec.Command(r.cfg.fixBinary, "-addr", addr)
	// Neither credentials nor JWTs appear in argv, logs, events, or inherited env.
	cmd.Env = []string{"FXP_JWT_SECRET=" + r.cfg.secret, "FXP_FIX_CREDENTIALS=" + comp + ":" + password, "FXP_FIX_STORE_DIR=" + dir, "FXP_FIX_STORE_FSYNC=strict", "FXP_FIX_GATEWAY_ADDR=" + r.cfg.simGateway, "FXP_LOG_LEVEL=error"}
	for _, key := range []string{"PATH", "FXP_JWT_ISSUER", "FXP_JWT_AUDIENCE", "FXP_JWT_KID"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	for _, suffix := range []string{"TLS", "TLS_CA_FILE", "TLS_CERT_FILE", "TLS_KEY_FILE", "TLS_SERVER_NAME"} {
		if value, ok := os.LookupEnv("FXP_CLIENT_" + suffix); ok {
			cmd.Env = append(cmd.Env, "FXP_GATEWAY_"+suffix+"="+value)
		}
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if e = cmd.Start(); e != nil {
		return nil, "", errors.New("owned fix_bridge executable could not start")
	}
	p := &scenarioBridge{cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()
	stop := context.AfterFunc(ctx, func() { _ = p.kill() })
	r.cleanupExtra = append(r.cleanupExtra, func() error { stop(); return p.kill() })
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-p.done:
			return nil, "", errors.New("owned FIX bridge exited during startup")
		case <-deadline.C:
			return nil, "", errors.New("owned FIX bridge failed readiness")
		case <-tick.C:
			conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				return p, addr, nil
			}
		}
	}
}

type scenarioFIXClient struct {
	conn   net.Conn
	parser *codec.Parser
	comp   string
	seq    int
}

func newScenarioFIX(ctx context.Context, addr, comp string, seq int) (*scenarioFIXClient, error) {
	conn, e := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if e != nil {
		return nil, e
	}
	context.AfterFunc(ctx, func() { _ = conn.Close() })
	return &scenarioFIXClient{conn: conn, parser: codec.NewParser(conn), comp: comp, seq: seq}, nil
}
func (c *scenarioFIXClient) send(typ string, fields [][2]string) error {
	b := codec.NewBuilder(typ, c.comp, "FXPBRIDGE", c.seq)
	for _, f := range fields {
		tag, e := strconv.Atoi(f[0])
		if e != nil {
			return e
		}
		b.Set(tag, f[1])
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_, e := c.conn.Write(b.Build())
	c.seq++
	return e
}
func (c *scenarioFIXClient) wait(ctx context.Context, name string, match func(*codec.Message) bool) (*codec.Message, error) {
	deadline := time.Now().Add(6 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.conn.SetReadDeadline(deadline)
	for {
		m, e := c.parser.ReadMessage()
		if e != nil {
			return nil, fmt.Errorf("FIX: no %s before deadline", name)
		}
		if match(m) {
			return m, nil
		}
		if m.MsgType() == "3" || m.MsgType() == "j" || m.MsgType() == "5" {
			return nil, fmt.Errorf("FIX rejected %s: %s", name, m.Get(58))
		}
	}
}
func (r *scenarioRun) fixRecovery(observer *scenarioSession) error {
	dir, e := os.MkdirTemp("", "fxp-scenario-store-")
	if e != nil {
		return errors.New("cannot create private FIX store")
	}
	r.cleanupExtra = append(r.cleanupExtra, func() error { return os.RemoveAll(dir) })
	var secret [24]byte
	if _, e = rand.Read(secret[:]); e != nil {
		return errors.New("cannot create FIX credential")
	}
	password := hex.EncodeToString(secret[:])
	comp := "SCN" + r.ID
	r.externalOwners = append(r.externalOwners, comp)
	var bridge *scenarioBridge
	var client *scenarioFIXClient
	var original *codec.Message
	nextIn := 3
	if e = r.step("Start a private bridge and persist an order ACK", "blotter", func() (string, error) {
		var addr string
		var e error
		bridge, addr, e = r.launchBridge(r.ctx, dir, comp, password)
		if e != nil {
			return "", e
		}
		client, e = newScenarioFIX(r.ctx, addr, comp, 1)
		if e != nil {
			return "", e
		}
		initialClient := client
		r.cleanupExtra = append(r.cleanupExtra, func() error { _ = initialClient.conn.Close(); return nil })
		if e = client.send("A", [][2]string{{"98", "0"}, {"108", "30"}, {"141", "Y"}, {"554", password}}); e != nil {
			return "", e
		}
		ack, e := client.wait(r.ctx, "initial LogonAck", func(m *codec.Message) bool { return m.MsgType() == "A" })
		if e != nil {
			return "", e
		}
		if e = assertScenario(ack.Get(34) == "1", "expected initial FIX outbound seq 1; got %s", ack.Get(34)); e != nil {
			return "", e
		}
		id := r.ID + "-fix-order"
		price := fmt.Sprintf("%d.%02d", r.price/100, r.price%100)
		if e = client.send("D", [][2]string{{"11", id}, {"55", r.Symbol}, {"54", "1"}, {"38", strconv.Itoa(int(r.qty))}, {"40", "2"}, {"44", price}, {"59", "1"}, {"60", time.Now().UTC().Format("20060102-15:04:05.000")}}); e != nil {
			return "", e
		}
		original, e = client.wait(r.ctx, "persisted EXEC_NEW", func(m *codec.Message) bool { return m.MsgType() == "8" && m.Get(11) == id && m.Get(150) == "0" })
		if e != nil {
			return "", e
		}
		if e = assertScenario(original.Get(34) == "2" && original.Get(17) != "", "expected persisted order ACK at sequence 2 with ExecID"); e != nil {
			return "", e
		}
		if e = r.observeFIXOrder(comp, id); e != nil {
			return "", e
		}
		snap, e := r.snapshot(r.ctx, observer)
		if e != nil {
			return "", e
		}
		if e = assertScenario(len(snap.BidLevels) == 1 && snap.BidLevels[0].Price == r.price && snap.BidLevels[0].Size == r.qty, "FIX order not resting at expected price/quantity"); e != nil {
			return "", e
		}
		return fmt.Sprintf("Private loopback bridge; strict temporary FileStore; Logon seq=1, order ACK seq=2; quantity=%d", r.qty), nil
	}); e != nil {
		return e
	}
	if e = r.step("Kill the bridge and restart with the same FileStore", "blotter", func() (string, error) {
		if e := bridge.kill(); e != nil {
			return "", e
		}
		_ = client.conn.Close()
		_, addr, e := r.launchBridge(r.ctx, dir, comp, password)
		if e != nil {
			return "", e
		}
		client, e = newScenarioFIX(r.ctx, addr, comp, nextIn)
		if e != nil {
			return "", e
		}
		resumedClient := client
		r.cleanupExtra = append(r.cleanupExtra, func() error { _ = resumedClient.conn.Close(); return nil })
		if e = client.send("A", [][2]string{{"98", "0"}, {"108", "30"}, {"554", password}}); e != nil {
			return "", e
		}
		ack, e := client.wait(r.ctx, "recovered LogonAck", func(m *codec.Message) bool { return m.MsgType() == "A" })
		if e != nil {
			return "", e
		}
		return "Killed and reaped bridge; restart accepted inbound seq=3 and emitted outbound seq=3", assertScenario(ack.Get(34) == "3", "expected resumed outbound seq=3; got %s", ack.Get(34))
	}); e != nil {
		return e
	}
	return r.step("Resend the original stored execution report", "blotter", func() (string, error) {
		if e := client.send("2", [][2]string{{"7", "2"}, {"16", "2"}}); e != nil {
			return "", e
		}
		replay, e := client.wait(r.ctx, "PossDup replay at original sequence 2", func(m *codec.Message) bool { return m.MsgType() == "8" && m.Get(34) == "2" })
		if e != nil {
			return "", e
		}
		equal := replay.Get(43) == "Y" && replay.Get(122) == original.Get(52)
		for _, field := range original.Tags {
			switch field.Tag {
			case 9, 10, 52:
				continue
			}
			equal = equal && replay.Get(field.Tag) == field.Value
		}
		if e = assertScenario(equal, "resend differs from persisted ACK or is missing PossDup/OrigSendingTime"); e != nil {
			return "", e
		}
		r.emit(scenarioEvent{Kind: "evidence", Panel: "blotter", Message: fmt.Sprintf("Original ACK: 34=%s | 17=%s | 11=%s\nRecovered resend: 34=%s | 43=%s | 17=%s | 122=%s", original.Get(34), original.Get(17), original.Get(11), replay.Get(34), replay.Get(43), replay.Get(17), replay.Get(122))})
		// Replay must not allocate a new outbound sequence. A heartbeat is the barrier.
		if e = client.send("1", [][2]string{{"112", "barrier"}}); e != nil {
			return "", e
		}
		heartbeat, e := client.wait(r.ctx, "post-replay heartbeat", func(m *codec.Message) bool { return m.MsgType() == "0" && m.Get(112) == "barrier" })
		if e != nil {
			return "", e
		}
		if e = assertScenario(heartbeat.Get(34) == "4", "replay consumed sequence numbers; next heartbeat=%s", heartbeat.Get(34)); e != nil {
			return "", e
		}
		return "Resend seq=2 preserves ExecID and order fields; PossDup=Y; OrigSendingTime retained; next outbound seq=4", nil
	})
}

func (r *scenarioRun) observeFIXOrder(owner, id string) error {
	monitor, e := r.connectToken(r.ctx, "fix-user", false, func() (string, error) { return devjwt.GenerateWithTTL(r.cfg.secret, owner, time.Minute) })
	if e != nil {
		return e
	}
	defer monitor.close()
	request := r.ID + "-fix-status"
	if e = monitor.send(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderStatusRequest{OrderStatusRequest: &protobuf.OrderStatusRequest{RequestId: request, OrderId: id, Symbol: r.Symbol}}}); e != nil {
		return e
	}
	msg, e := monitor.wait(r.ctx, 0, "retained FIX order for observer blotter", func(m *protobuf.FXPMessage) bool {
		return m.GetOrderStatusResponse() != nil && m.GetOrderStatusResponse().RequestId == request && m.GetOrderStatusResponse().Complete
	})
	if e != nil {
		return e
	}
	rep := msg.GetOrderStatusResponse()
	return assertScenario(rep.RejectReason == "" && len(rep.Reports) == 1 && rep.Reports[0].OrderStatus == protobuf.OrderStatus_ORDER_NEW && rep.Reports[0].LeavesQuantity == r.qty, "retained FIX order status differs from ACK")
}
