// SPDX-License-Identifier: MIT

// main.go — FXP FIX Bridge
//
// Accepts inbound FIX 4.4 TCP connections and translates them to FXP Protocol,
// forwarding to the FXP Go Gateway. Translates FXP ExecutionReports and market
// data back to FIX for delivery to the originating client.
//
// Architecture:
//
//   FIX Client (TCP :8083)
//       │  FIX 4.4 tag=value frames
//       ▼
//   fix_bridge (this process)
//       ├── codec:      FIX parser + builder
//       ├── session:    Logon/Logout/Heartbeat/SeqNum state machine
//       ├── translator: FIX↔FXP field mapping
//       └── gateway:    FXP Protobuf client to Go Gateway
//       │  FXP Protobuf length-prefixed frames
//       ▼
//   Go Gateway (:8082) → Rust Core (:8080)
//
// One goroutine set per FIX client:
//   - session.readLoop  — reads FIX frames, validates seqnums, dispatches
//   - session.writeLoop — drains outbound FIX frame channel to TCP socket
//   - session.heartbeatLoop — sends Heartbeat every HeartBtInt seconds
//   - gateway.readLoop  — reads FXP frames, translates, sends to FIX client
//   - gateway.writeLoop — drains outbound FXP frame channel to gateway TCP
//
// Usage:
//   export FXP_JWT_SECRET="dev-secret-change-in-production"
//   go run fix_bridge/main.go
//   go run fix_bridge/main.go --addr :8083 --gateway 127.0.0.1:8082
//
// Prerequisites:
//   cargo run          (Rust Core, terminal 1)
//   go run main.go     (Go Gateway, terminal 2)

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"fix_bridge/codec"
	"fix_bridge/gateway"
	"fix_bridge/session"
	"fix_bridge/translator"

	"go_gateway/logging"
	"go_gateway/protobuf"
	"go_gateway/symbolconfig"
	"go_gateway/tlsconfig"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// ── Config ────────────────────────────────────────────────────────────────────

var (
	listenAddr   = flag.String("addr", ":8083", "FIX TCP listen address")
	bridgeCompID = flag.String("compid", "FXPBRIDGE", "Bridge SenderCompID in outbound FIX messages")
)

// ── Entry point ───────────────────────────────────────────────────────────────

func main() {
	logging.Setup()
	if err := symbolconfig.Configure(os.Getenv("FXP_SYMBOL_CONFIG")); err != nil {
		log.Fatalf("FXP_SYMBOL_CONFIG: %v", err)
	}
	flag.Parse()
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	manager, err := tlsconfig.ServerFromEnv()
	if err != nil {
		log.Fatalf("TLS configuration: %v", err)
	}
	upstream, err := tlsconfig.ClientFromEnv("FXP_GATEWAY_")
	if err != nil {
		log.Fatalf("gateway TLS configuration: %v", err)
	}
	if _, err = upstream.Config(); err != nil {
		log.Fatalf("gateway TLS configuration: %v", err)
	}
	go manager.Watch(ctx, time.Second)
	configCheck := session.NewSession(nil, "", nil)
	if err := configCheck.ConfigureSchedule(); err != nil {
		log.Fatalf("FIX schedule: %v", err)
	}
	jwtSecret := os.Getenv("FXP_JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("FXP_JWT_SECRET environment variable not set")
	}
	// FXP_FIX_CREDENTIALS="COMPID:password,..." — the Logon allow-list.
	creds, err := session.ParseCredentials(os.Getenv("FXP_FIX_CREDENTIALS"))
	if err != nil {
		log.Fatalf("FXP_FIX_CREDENTIALS: %v", err)
	}
	auth := session.NewCredentialAuth(creds)
	registry, err := newSessionRegistry()
	if err != nil {
		log.Fatalf("FIX session store: %v", err)
	}

	listener, err := manager.Listen(*listenAddr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", *listenAddr, err)
	}
	log.Printf("🌉 FXP FIX Bridge listening on %s", *listenAddr)
	log.Printf("   SenderCompID: %s", *bridgeCompID)
	log.Printf("   Gateway:      127.0.0.1:8082")
	log.Printf("   FXP_JWT_SECRET: set ✓")
	log.Printf("   FIX CompIDs allowed: %d", len(creds))

	sessions := newSessionTracker()
	acceptDone := make(chan struct{})
	go func() {
		acceptFIXClientsContext(ctx, listener, fixMaxConnections(), func(conn net.Conn) {
			log.Printf("📞 FIX client connected from %s", conn.RemoteAddr())
			handleFIXClient(conn, *bridgeCompID, jwtSecret, auth, registry, sessions)
		})
		close(acceptDone)
	}()

	select {
	case <-ctx.Done():
		log.Printf("received shutdown signal; draining FIX sessions")
	case <-acceptDone:
		log.Printf("FIX accept loop stopped; draining remaining sessions")
	}
	timeout := time.Duration(positiveIntEnv("FXP_SHUTDOWN_TIMEOUT_SEC", 5)) * time.Second
	if err := shutdownSessions(sessions, acceptDone, timeout); err != nil {
		log.Printf("%v", err)
	}
	if err := registry.Close(); err != nil {
		log.Printf("FIX session store shutdown failed: %v", err)
		os.Exit(1)
	}
	log.Printf("FIX bridge shutdown completed")
}

func shutdownSessions(tracker *sessionTracker, acceptDone <-chan struct{}, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	tracker.beginShutdown()
	select {
	case <-acceptDone:
		return nil
	case <-timer.C:
		tracker.closeAll()
		<-acceptDone
		return fmt.Errorf("FIX session shutdown exceeded %v; remaining sessions closed", timeout)
	}
}

// newSessionRegistry keeps FIX session state (sequence numbers, sent messages)
// per (BeginString, SenderCompID, TargetCompID) across reconnects and restarts.
//
//	FXP_FIX_STORE_DIR            directory (default "fix_store"; set empty for in-memory)
//	FXP_FIX_STORE_FSYNC          strict (default) | group | async
//	FXP_FIX_STORE_GROUP_INTERVAL group fsync interval (default 10ms)
//	FXP_FIX_STORE_MAX_MESSAGES   outbound messages retained for resend (default 100000)
func newSessionRegistry() (*session.Registry, error) {
	dir, ok := os.LookupEnv("FXP_FIX_STORE_DIR")
	if !ok {
		dir = "fix_store"
	}
	if dir == "" {
		log.Printf("⚠️ FXP_FIX_STORE_DIR is empty — FIX session state is lost on restart")
		return session.NewRegistry(session.MemoryStoreFactory), nil
	}
	mode, err := session.ParseFsyncMode(os.Getenv("FXP_FIX_STORE_FSYNC"))
	if err != nil {
		return nil, err
	}
	opts := session.FileStoreOptions{
		Fsync:         mode,
		GroupInterval: sessionDurationEnv("FXP_FIX_STORE_GROUP_INTERVAL", 0),
		MaxMessages:   positiveIntEnv("FXP_FIX_STORE_MAX_MESSAGES", 100_000),
	}
	log.Printf("   FIX store: %s (fsync=%s, retain %d messages)", dir, mode, opts.MaxMessages)
	return session.NewRegistry(session.FileStoreFactory(dir, opts)), nil
}

func positiveIntEnv(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		log.Printf("⚠️ ignoring invalid %s=%q, using %d", name, value, fallback)
		return fallback
	}
	return n
}

func fixMaxConnections() int {
	const defaultMax = 1024
	value := os.Getenv("FXP_FIX_MAX_CONNS")
	if value == "" {
		return defaultMax
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		log.Printf("⚠️ ignoring invalid FXP_FIX_MAX_CONNS=%q, using %d", value, defaultMax)
		return defaultMax
	}
	return n
}

func acceptFIXClients(listener net.Listener, maxConnections int, handle func(net.Conn)) {
	acceptFIXClientsContext(context.Background(), listener, maxConnections, handle)
}

func acceptFIXClientsContext(ctx context.Context, listener net.Listener, maxConnections int, handle func(net.Conn)) {
	if maxConnections <= 0 {
		maxConnections = 1
	}
	slots := make(chan struct{}, maxConnections)
	var handlers sync.WaitGroup
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-stopped:
		}
	}()
	defer close(stopped)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				handlers.Wait()
				return
			}
			log.Printf("accept error: %v", err)
			handlers.Wait()
			return
		}
		select {
		case slots <- struct{}{}:
			handlers.Add(1)
			go func(conn net.Conn) {
				defer handlers.Done()
				// Free the slot before closing so a client that sees EOF can reconnect immediately.
				defer func() {
					<-slots
					_ = conn.Close()
				}()
				if err := tlsconfig.Handshake(ctx, conn, 10*time.Second); err != nil {
					return
				}
				handle(conn)
			}(conn)
		default:
			log.Printf("FIX connection limit reached (%d); closing %s", maxConnections, conn.RemoteAddr())
			_ = conn.Close()
		}
	}
}

type sessionTracker struct {
	mu       sync.Mutex
	sessions map[*session.Session]struct{}
	stopping bool
}

func newSessionTracker() *sessionTracker {
	return &sessionTracker{sessions: make(map[*session.Session]struct{})}
}

func (t *sessionTracker) track(sess *session.Session) {
	t.mu.Lock()
	t.sessions[sess] = struct{}{}
	stopping := t.stopping
	t.mu.Unlock()
	if stopping {
		go t.requestShutdown(sess)
	}
}

func (t *sessionTracker) untrack(sess *session.Session) {
	t.mu.Lock()
	delete(t.sessions, sess)
	t.mu.Unlock()
}

func (t *sessionTracker) beginShutdown() {
	t.mu.Lock()
	t.stopping = true
	sessions := make([]*session.Session, 0, len(t.sessions))
	for sess := range t.sessions {
		sessions = append(sessions, sess)
	}
	t.mu.Unlock()
	for _, sess := range sessions {
		go t.requestShutdown(sess)
	}
}

func (t *sessionTracker) closeAll() {
	t.mu.Lock()
	sessions := make([]*session.Session, 0, len(t.sessions))
	for sess := range t.sessions {
		sessions = append(sessions, sess)
	}
	t.mu.Unlock()
	for _, sess := range sessions {
		sess.Close()
	}
}

func (t *sessionTracker) requestShutdown(sess *session.Session) {
	switch sess.State() {
	case session.StateLoggedOn:
		sess.Logout("server shutdown")
	case session.StateLoggingOut:
	default:
		sess.Close()
	}
}

// ── Per-client handler ────────────────────────────────────────────────────────

// handleFIXClient runs the full lifecycle for one FIX client connection.
// Creates a session, a gateway client, wires them together, and blocks
// until the FIX session ends.
func handleFIXClient(conn net.Conn, bridgeCompID, jwtSecret string, auth session.Authenticator, registry *session.Registry, sessions *sessionTracker) {
	// The bridge handler implements session.Handler.
	// It holds references to both the session (to send FIX frames back)
	// and the gateway client (to forward orders).
	h := &bridgeHandler{
		bridgeCompID: bridgeCompID,
		jwtSecret:    jwtSecret,
	}

	// Create the FIX session — TargetCompID is filled in when Logon arrives
	sess := session.NewSession(conn, bridgeCompID, h)
	sess.LogonTimeout = sessionDurationEnv("FXP_FIX_LOGON_TIMEOUT", sess.LogonTimeout)
	sess.LogoutTimeout = sessionDurationEnv("FXP_FIX_LOGOUT_TIMEOUT", sess.LogoutTimeout)
	sess.MaxLatency = sessionDurationEnv("FXP_FIX_MAX_LATENCY", sess.MaxLatency)
	if err := sess.ConfigureSchedule(); err != nil {
		log.Printf("FIX schedule: %v", err)
		_ = conn.Close()
		return
	}
	sess.Auth = auth
	sess.Registry = registry
	h.sess = sess
	sessions.track(sess)
	defer sessions.untrack(sess)

	// Run the session (blocks until disconnect)
	sess.Run()
}

// ── Bridge handler ────────────────────────────────────────────────────────────

// bridgeHandler implements session.Handler. It connects to the FXP gateway
// on Logon, forwards application messages, and routes ExecutionReports back
// to the FIX client.
type bridgeHandler struct {
	statusPending sync.Map
	statusSerial  atomic.Uint64
	bridgeCompID  string
	jwtSecret     string
	sess          *session.Session
	gwClient      *gateway.Client
	pending       pendingRequests
	md            mdSubscriptions
	cod           codState
}

// codRequestID marks the bridge's own cancel-on-disconnect mass cancel.
const codRequestID = "FXP-COD"

// codTimeout bounds how long a closed COD session waits for its cancels.
const codTimeout = 2 * time.Second

// codState tracks the cancel-on-disconnect mass cancel sent when a COD
// session ends; done closes once its report and all its canceled ERs arrived.
type codState struct {
	mu       sync.Mutex
	active   bool
	total    int // -1 until the report arrives
	canceled int
	done     chan struct{}
}

func (c *codState) start() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active, c.total, c.canceled, c.done = true, -1, 0, make(chan struct{})
	return c.done
}

// observeReport consumes the COD mass-cancel report, which is not forwarded.
func (c *codState) observeReport(msg *protobuf.FXPMessage) bool {
	rep := msg.GetOrderMassCancelReport()
	if rep == nil || rep.ClientRequestId != codRequestID {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return false
	}
	c.total = int(rep.TotalAffected)
	c.finishLocked()
	return true
}

// countCancel counts a COD cancel. Call it only once the ER is in the Store,
// so completion implies every cancel is available for resend.
func (c *codState) countCancel(msg *protobuf.FXPMessage) {
	er := msg.GetExecutionReport()
	if er == nil || er.ExecutionType != protobuf.ExecutionType_EXEC_CANCELED || er.RejectReason != "cancel on disconnect" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return
	}
	c.canceled++
	c.finishLocked()
}

func (c *codState) finishLocked() {
	if c.total >= 0 && c.canceled >= c.total {
		c.active = false
		close(c.done)
	}
}

// OnLogon is called by the session after Logon is accepted.
// This is when we connect to the FXP gateway — not before, because we need
// the client's SenderCompID (which becomes the FXP traderID) from the Logon.
func (h *bridgeHandler) OnLogon(sess *session.Session) {
	traderID := sess.TargetCompID
	log.Printf("[%s] OnLogon — connecting to FXP gateway as %s", traderID, traderID)

	// Create the gateway client. onReceive is called for every inbound FXP message.
	h.gwClient = gateway.NewClient(traderID, h.jwtSecret, func(msg *protobuf.FXPMessage) {
		h.onFXPMessage(msg)
	})
	h.gwClient.CancelOnDisconnect = sess.CancelOnDisconnect

	// Connect to gateway in a separate goroutine — Connect() blocks until disconnect.
	go func() {
		if err := h.gwClient.Connect(); err != nil {
			log.Printf("[%s] gateway connection failed: %v", traderID, err)
			// Orders queued while connecting are rejected, never dropped.
			h.gwClient.Close()
			for _, m := range h.gwClient.DrainPending() {
				if req := fixRequestFor(m); req != nil {
					h.sendBusinessReject(req, h.sess, "gateway unavailable")
				}
			}
			h.sess.Logout(fmt.Sprintf("gateway unavailable: %v", err))
		}
	}()
}

func sessionDurationEnv(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < 0 {
		log.Printf("⚠️ ignoring invalid %s=%q, using %v", name, value, fallback)
		return fallback
	}
	return d
}

// OnLogout is called by the session when the FIX client logs out or disconnects.
func (h *bridgeHandler) OnLogout(sess *session.Session, reason string) {
	if sess.CancelOnDisconnect {
		log.Printf("[%s] OnLogout: %q — cancel-on-disconnect runs when the connection closes", sess.TargetCompID, reason)
		return
	}
	log.Printf("[%s] OnLogout: %q — closing gateway connection", sess.TargetCompID, reason)
	if h.gwClient != nil {
		h.gwClient.Close()
	}
}

// OnDisconnect runs after the FIX connection closed for any reason. For a
// cancel-on-disconnect session it mass-cancels the trader's orders first; the
// canceled ERs go through SendWithSeq, so a durable session stores them for
// resend after reconnect. The gateway's own COD is the backstop if this
// bridge's link to it fails.
func (h *bridgeHandler) OnDisconnect(sess *session.Session) {
	if h.gwClient == nil {
		return
	}
	defer h.gwClient.Close()
	if !sess.CancelOnDisconnect {
		return
	}
	done := h.cod.start()
	err := h.gwClient.Send(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderMassCancel{
		OrderMassCancel: &protobuf.OrderMassCancelRequest{
			Sender:          sess.TargetCompID,
			ClientRequestId: codRequestID,
			Reason:          protobuf.MassCancelReason_MASS_CANCEL_REASON_DISCONNECT,
			Timestamp:       timestamppb.Now(),
		},
	}})
	if err != nil {
		log.Printf("[%s] cancel-on-disconnect not sent: %v (gateway COD applies)", sess.TargetCompID, err)
		return
	}
	select {
	case <-done:
		log.Printf("[%s] cancel-on-disconnect complete", sess.TargetCompID)
	case <-time.After(codTimeout):
		log.Printf("[%s] cancel-on-disconnect: timed out waiting for cancels", sess.TargetCompID)
	}
}

// Handle is called by the session for each inbound application message.
// Translates the FIX message to FXP and forwards to the gateway.
func (h *bridgeHandler) Handle(msg *codec.Message, sess *session.Session) {
	if !supportedAppMsgTypes[msg.MsgType()] {
		sess.SendWithSeq(func(seq int) []byte {
			return buildUnsupportedReject(msg, h.bridgeCompID, sess.TargetCompID, seq)
		})
		return
	}
	if h.gwClient == nil {
		h.sendBusinessReject(msg, sess, "gateway not connected")
		return
	}

	if msg.MsgType() == codec.MsgTypeMarketDataRequest && msg.Get(codec.TagSubscriptionRequestType) == "2" {
		h.md.unsubscribe(msg.Get(codec.TagMDReqID), mdSymbols(msg))
		return // nothing to forward upstream
	}

	fxpMsg, err := translator.TranslateFIXtoFXP(msg)
	if err != nil {
		slog.Warn("translation error", "session", sess.TargetCompID, "msg_type", msg.MsgType(), "err", err)
		var mcErr *translator.MassCancelRejectError
		if errors.As(err, &mcErr) {
			sess.SendWithSeq(func(seq int) []byte {
				return translator.MassCancelReport(h.bridgeCompID, sess.TargetCompID, seq,
					msg.Get(codec.TagClOrdID), msg.Get(codec.TagMassCancelRequestType), msg.Get(codec.TagSymbol),
					protobuf.OrderSide_ORDER_SIDE_UNSPECIFIED, 0, mcErr.Reason, mcErr.Text)
			})
			return
		}
		h.sendBusinessReject(msg, sess, err.Error())
		return
	}

	// Recorded before sending so the snapshot cannot outrun it; undone on failure.
	var undoMD func()
	if mdReq := fxpMsg.GetMarketDataRequest(); mdReq != nil {
		stream := msg.Get(codec.TagSubscriptionRequestType) == "1"
		undoMD = h.md.subscribe(msg.Get(codec.TagMDReqID), stream, mdReq.Symbols)
	}

	if q := fxpMsg.GetOrderStatusRequest(); q != nil {
		h.trackStatus(msg, q)
	}
	req := fixRequestFor(fxpMsg)
	tracked := req != nil && isCancelOrReplace(req)
	if tracked {
		h.pending.add(req)
	}
	if err := h.gwClient.Send(fxpMsg); err != nil {
		if q := fxpMsg.GetOrderStatusRequest(); q != nil {
			h.statusPending.Delete(q.RequestId)
		}
		if tracked {
			h.pending.take(req.Get(codec.TagClOrdID))
		}
		if undoMD != nil {
			undoMD()
		}
		slog.Warn("gateway send error", "session", sess.TargetCompID, "err", err)
		h.sendBusinessReject(msg, sess, "gateway unavailable: "+err.Error())
	}
}

// onFXPMessage is called by the gateway client for every inbound FXP frame.
// Translates the FXP message to FIX and delivers to the FIX client.
func (h *bridgeHandler) onFXPMessage(msg *protobuf.FXPMessage) {
	if rep := msg.GetOrderStatusResponse(); rep != nil {
		h.onStatus(rep)
		return
	}
	if h.cod.observeReport(msg) {
		return
	}
	defer h.cod.countCancel(msg) // after SendWithSeq stored the ER
	if rep := msg.GetExecutionReport(); rep != nil {
		if req := h.pending.cancelRequestReject(rep); req != nil {
			h.sess.SendWithSeq(func(seq int) []byte {
				return buildCancelReject(req, rep.OrderId, h.bridgeCompID, h.sess.TargetCompID, seq, rep.RejectReason)
			})
			return
		}
	}
	if symbol, snapshot, ok := marketDataSymbol(msg); ok {
		ids := h.md.incrementalTargets(symbol)
		if snapshot {
			ids = h.md.snapshotTargets(symbol)
		}
		for _, id := range ids {
			h.sess.SendWithSeq(func(seq int) []byte {
				return translator.TranslateFXPtoFIX(msg, h.bridgeCompID, h.sess.TargetCompID, seq,
					func(string) string { return id })
			})
		}
		return
	}
	// Sequence numbers come from the session; untranslated types (nil) consume none.
	h.sess.SendWithSeq(func(seq int) []byte {
		return translator.TranslateFXPtoFIX(msg, h.bridgeCompID, h.sess.TargetCompID, seq, nil)
	})
}

func marketDataSymbol(msg *protobuf.FXPMessage) (symbol string, snapshot, ok bool) {
	switch p := msg.Payload.(type) {
	case *protobuf.FXPMessage_MarketDataSnapshot:
		return p.MarketDataSnapshot.Symbol, true, true
	case *protobuf.FXPMessage_MarketDataIncremental:
		return p.MarketDataIncremental.Symbol, false, true
	}
	return "", false, false
}

// sendBusinessReject sends a FIX ExecutionReport with OrdStatus=Rejected
// when a NewOrderSingle cannot be translated.
// For cancel/replace failures, sends an OrderCancelReject.
func (h *bridgeHandler) sendBusinessReject(msg *codec.Message, sess *session.Session, reason string) {
	sess.SendWithSeq(func(seq int) []byte {
		return buildBusinessReject(msg, h.bridgeCompID, sess.TargetCompID, seq, reason)
	})
}

func buildBusinessReject(msg *codec.Message, bridgeCompID, targetCompID string, seq int, reason string) []byte {
	switch msg.MsgType() {
	case codec.MsgTypeNewOrderSingle:
		b := codec.NewBuilder(codec.MsgTypeExecutionReport, bridgeCompID, targetCompID, seq)
		b.Set(codec.TagOrderID, "NONE")
		b.Set(codec.TagExecID, fmt.Sprintf("REJ-%s", msg.Get(codec.TagClOrdID)))
		b.Set(codec.TagExecType, codec.ExecTypeRejected)
		b.Set(codec.TagOrdStatus, codec.OrdStatusRejected)
		b.Set(codec.TagClOrdID, msg.Get(codec.TagClOrdID))
		b.Set(codec.TagSymbol, msg.Get(codec.TagSymbol))
		b.SetInt(codec.TagLeavesQty, 0)
		b.SetInt(codec.TagCumQty, 0)
		b.Set(codec.TagOrdRejReason, codec.OrdRejReasonOther)
		b.Set(codec.TagText, reason)
		return b.Build()

	case codec.MsgTypeOrderCancelRequest, codec.MsgTypeOrderCancelReplaceRequest:
		return buildCancelReject(msg, "NONE", bridgeCompID, targetCompID, seq, reason)

	default:
		b := codec.NewBuilder(codec.MsgTypeBusinessMessageReject, bridgeCompID, targetCompID, seq)
		if ref := msg.Get(codec.TagMsgSeqNum); ref != "" {
			b.Set(codec.TagRefSeqNum, ref)
		}
		b.Set(codec.TagRefMsgType, msg.MsgType())
		b.Set(codec.TagBusinessRejectReason, codec.BusinessRejectReasonOther)
		b.Set(codec.TagText, reason)
		return b.Build()
	}
}

// supportedAppMsgTypes are the inbound application messages the bridge
// translates; every other application MsgType gets BusinessMessageReject.
var supportedAppMsgTypes = map[string]bool{
	"H": true, "AF": true,
	codec.MsgTypeNewOrderSingle:            true,
	codec.MsgTypeOrderCancelRequest:        true,
	codec.MsgTypeOrderCancelReplaceRequest: true,
	codec.MsgTypeMarketDataRequest:         true,
	codec.MsgTypeOrderMassCancelRequest:    true,
}

// unsupportedMsgTypeNames names common FIX 4.4 requests for reject texts.
var unsupportedMsgTypeNames = map[string]string{
	codec.MsgTypeOrderStatusRequest: "OrderStatusRequest",
	"AF":                            "OrderMassStatusRequest",
	"q":                             "OrderMassCancelRequest",
	"AD":                            "TradeCaptureReportRequest",
	"c":                             "SecurityDefinitionRequest",
	"x":                             "SecurityListRequest",
}

// buildUnsupportedReject answers an unsupported application message with
// BusinessMessageReject(j), BusinessRejectReason(380)=3.
func buildUnsupportedReject(msg *codec.Message, bridgeCompID, targetCompID string, seq int) []byte {
	name := msg.MsgType()
	if n, ok := unsupportedMsgTypeNames[name]; ok {
		name += " (" + n + ")"
	}
	b := codec.NewBuilder(codec.MsgTypeBusinessMessageReject, bridgeCompID, targetCompID, seq)
	if ref := msg.Get(codec.TagMsgSeqNum); ref != "" {
		b.Set(codec.TagRefSeqNum, ref)
	}
	b.Set(codec.TagRefMsgType, msg.MsgType())
	b.Set(codec.TagBusinessRejectReason, codec.BusinessRejectReasonUnsupportedMsgType)
	b.Set(codec.TagText, "Unsupported MsgType "+name+"; supported: D, F, G, V, q, H, AF")
	return b.Build()
}

// fixRequestFor reconstructs the identifying FIX fields of a queued FXP
// request so it can be rejected with buildBusinessReject.
func fixRequestFor(m *protobuf.FXPMessage) *codec.Message {
	f := map[int]string{}
	switch p := m.Payload.(type) {
	case *protobuf.FXPMessage_NewOrder:
		f[codec.TagMsgType] = codec.MsgTypeNewOrderSingle
		f[codec.TagClOrdID] = p.NewOrder.OrderId
		f[codec.TagSymbol] = p.NewOrder.Symbol
	case *protobuf.FXPMessage_OrderCancel:
		f[codec.TagMsgType] = codec.MsgTypeOrderCancelRequest
		f[codec.TagClOrdID] = p.OrderCancel.ClientCancelId
		f[codec.TagOrigClOrdID] = p.OrderCancel.OrderId
	case *protobuf.FXPMessage_OrderCancelReplace:
		f[codec.TagMsgType] = codec.MsgTypeOrderCancelReplaceRequest
		f[codec.TagClOrdID] = p.OrderCancelReplace.ClientReplaceId
		f[codec.TagOrigClOrdID] = p.OrderCancelReplace.OrderId
	case *protobuf.FXPMessage_OrderMassCancel:
		f[codec.TagMsgType] = codec.MsgTypeOrderMassCancelRequest
		f[codec.TagClOrdID] = p.OrderMassCancel.ClientRequestId
	default:
		return nil
	}
	return &codec.Message{Fields: f}
}

// mdSymbols returns the Symbol(55) entries of a MarketDataRequest.
func mdSymbols(msg *codec.Message) []string {
	var symbols []string
	for _, f := range msg.Tags {
		if f.Tag == codec.TagSymbol && f.Value != "" {
			symbols = append(symbols, f.Value)
		}
	}
	return symbols
}
