// SPDX-License-Identifier: MIT
// Package metrics provides an opt-in, bounded-cardinality Prometheus endpoint.
package metrics

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go_gateway/protobuf"
	"google.golang.org/protobuf/encoding/protowire"
)

var enabled atomic.Bool
var clients atomic.Int64
var incoming, outgoing [24]atomic.Uint64
var commands atomic.Uint64
var processed atomic.Uint64
var rejects [8]atomic.Uint64
var started = time.Now()

// Release builds override these with -ldflags; ordinary Git builds use build info.
var Version = "0.1.0"
var GitSHA = "unknown"
var types = [...]string{"unknown", "heartbeat", "logon", "logout", "gap_fill", "new_order", "order_acknowledgment", "order_cancel", "execution_report", "market_data_snapshot", "market_data_incremental", "market_data_request", "trade_capture_report", "error_message", "logon_ack", "order_cancel_replace", "trading_session_status", "auction_info", "order_mass_cancel", "order_mass_cancel_report", "order_status_request", "order_status_response", "session_control", "session_control_report"}
var reasons = [...]string{"authentication", "permission", "rate_limit", "capacity", "unknown_order", "duplicate", "invalid", "other"}

func Enabled() bool { return enabled.Load() }
func Client() func() {
	if !Enabled() {
		return func() {}
	}
	clients.Add(1)
	return func() { clients.Add(-1) }
}
func payload(raw []byte) (int, []byte) {
	n, t, size := protowire.ConsumeTag(raw)
	if size < 0 || t != protowire.BytesType || n < 1 || n > 23 {
		return 0, nil
	}
	b, size := protowire.ConsumeBytes(raw[size:])
	if size < 0 {
		return 0, nil
	}
	return int(n), b
}
func category(reason string) int {
	// Only fixed categories escape this function: never label with user text.
	r := strings.ToLower(reason)
	switch {
	case strings.Contains(r, "token"), strings.Contains(r, "logon"), strings.Contains(r, "auth"), strings.Contains(r, "session expired"):
		return 0
	case strings.Contains(r, "permission"), strings.Contains(r, "operator"), strings.Contains(r, "owner"), strings.Contains(r, "not owned"):
		return 1
	case strings.Contains(r, "rate limit"):
		return 2
	case strings.Contains(r, "limit"), strings.Contains(r, "maximum"), strings.Contains(r, "unavailable"), strings.Contains(r, "queue"):
		return 3
	case strings.Contains(r, "unknown order"), strings.Contains(r, "not found"):
		return 4
	case strings.Contains(r, "duplicate"):
		return 5
	case strings.Contains(r, "invalid"), strings.Contains(r, "required"), strings.Contains(r, "quantity"), strings.Contains(r, "price"), strings.Contains(r, "unknown"):
		return 6
	default:
		return 7
	}
}
func Raw(raw []byte, in bool) {
	if !Enabled() {
		return
	}
	kind, _ := payload(raw)
	if in {
		incoming[kind].Add(1)
		return
	}
	outgoing[kind].Add(1)
}

// Response observes already-decoded rejection responses before queuing. A slow
// or unauthenticated client can disconnect before a rejection is delivered.
func Response(m *protobuf.FXPMessage) {
	if !Enabled() {
		return
	}
	reason := ""
	switch p := m.Payload.(type) {
	case *protobuf.FXPMessage_ExecutionReport:
		if p.ExecutionReport.OrderStatus != protobuf.OrderStatus_ORDER_REJECTED {
			return
		}
		reason = p.ExecutionReport.RejectReason
		if reason == "" {
			reason = "rejected"
		}
	case *protobuf.FXPMessage_ErrorMessage:
		reason = p.ErrorMessage.ErrorMessage
	case *protobuf.FXPMessage_LogonAck:
		if !p.LogonAck.Accepted {
			rejects[0].Add(1)
		}
		return
	case *protobuf.FXPMessage_OrderMassCancelReport:
		reason = p.OrderMassCancelReport.RejectReason
	case *protobuf.FXPMessage_OrderStatusResponse:
		reason = p.OrderStatusResponse.RejectReason
	case *protobuf.FXPMessage_SessionControlReport:
		reason = p.SessionControlReport.RejectReason
	}
	if reason != "" {
		rejects[category(reason)].Add(1)
	}
}

// Processed counts supported commands dispatched by the authenticated router,
// including commands subsequently rejected by validation or core availability.
func Processed(m *protobuf.FXPMessage) {
	if !Enabled() {
		return
	}
	switch m.Payload.(type) {
	case *protobuf.FXPMessage_NewOrder, *protobuf.FXPMessage_OrderCancel, *protobuf.FXPMessage_OrderCancelReplace, *protobuf.FXPMessage_MarketDataRequest, *protobuf.FXPMessage_OrderMassCancel, *protobuf.FXPMessage_OrderStatusRequest, *protobuf.FXPMessage_SessionControl:
		processed.Add(1)
	}
}

// Command counts successful writes to core, not confirmed engine executions.
func Command(raw []byte) {
	if !Enabled() {
		return
	}
	kind, _ := payload(raw)
	switch kind {
	case 5, 7, 11, 15, 18, 20, 22:
		commands.Add(1)
	}
}
func buildSHA() string {
	if GitSHA != "unknown" {
		return GitSHA
	}
	if b, ok := debug.ReadBuildInfo(); ok {
		for _, s := range b.Settings {
			if s.Key == "vcs.revision" {
				return s.Value
			}
		}
	}
	return "unknown"
}
func render() string {
	var b strings.Builder
	metric := func(name, typ, help string) { fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ) }
	metric("fxp_connected_clients", "gauge", "Current client transport connections, including pre-Logon clients.")
	fmt.Fprintf(&b, "fxp_connected_clients %d\n", clients.Load())
	metric("fxp_messages_total", "counter", "Client wire messages successfully received or sent, by fixed protocol type.")
	for i, t := range types {
		fmt.Fprintf(&b, "fxp_messages_total{direction=\"in\",type=%q} %d\nfxp_messages_total{direction=\"out\",type=%q} %d\n", t, incoming[i].Load(), t, outgoing[i].Load())
	}
	metric("fxp_rejects_total", "counter", "Rejection responses generated or proxied, classified into fixed reason categories.")
	for i, r := range reasons {
		fmt.Fprintf(&b, "fxp_rejects_total{reason=%q} %d\n", r, rejects[i].Load())
	}
	metric("fxp_commands_processed_total", "counter", "Supported authenticated commands dispatched, including subsequent rejections.")
	fmt.Fprintf(&b, "fxp_commands_processed_total %d\n", processed.Load())
	metric("fxp_commands_forwarded_total", "counter", "Command frames successfully written to core; not execution confirmations.")
	fmt.Fprintf(&b, "fxp_commands_forwarded_total %d\n", commands.Load())
	metric("fxp_process_uptime_seconds", "gauge", "Seconds since process metrics initialization.")
	fmt.Fprintf(&b, "fxp_process_uptime_seconds %g\n", time.Since(started).Seconds())
	metric("fxp_build_info", "gauge", "Build version and Git revision; unknown when unavailable.")
	fmt.Fprintf(&b, "fxp_build_info{version=%q,git_sha=%q} 1\n", Version, buildSHA())
	return b.String()
}
func address(value string) (string, error) {
	if _, err := strconv.ParseUint(value, 10, 16); err == nil {
		return net.JoinHostPort("127.0.0.1", value), nil
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return "", fmt.Errorf("invalid metrics address")
	}
	if host == "" || host == "localhost" {
		host = "127.0.0.1"
	}
	if net.ParseIP(host) == nil {
		return "", fmt.Errorf("metrics address requires an IP literal")
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return "", fmt.Errorf("invalid metrics port")
	}
	return net.JoinHostPort(host, port), nil
}

const maxScrapeConnections = 32

// Admission happens in Accept, before net/http starts a connection goroutine.
// Excess connections are closed immediately, never queued behind the limit.
type scrapeListener struct {
	net.Listener
	slots chan struct{}
}

func (l *scrapeListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &scrapeConn{Conn: conn, release: func() { <-l.slots }}, nil
		default:
			_ = conn.Close()
		}
	}
}

type scrapeConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *scrapeConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// Start binds synchronously so configuration/bind failures fail startup. Empty disables.
func Start(ctx context.Context, value string) (func(), error) {
	if value == "" {
		return func() {}, nil
	}
	addr, err := address(value)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	enabled.Store(true)
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		if r.Method != "GET" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(render()))
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 8192}
	stopped := make(chan struct{})
	bounded := &scrapeListener{Listener: listener, slots: make(chan struct{}, maxScrapeConnections)}
	go func() { _ = server.Serve(bounded); close(stopped) }()
	go func() {
		select {
		case <-ctx.Done():
			_ = server.Close()
		case <-stopped:
		}
	}()
	return func() { _ = server.Close(); <-stopped }, nil
}
