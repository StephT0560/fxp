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
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"strconv"
	"time"

	"fix_bridge/codec"
	"fix_bridge/gateway"
	"fix_bridge/session"
	"fix_bridge/translator"

	"go_gateway/logging"
	"go_gateway/protobuf"
)

// ── Config ────────────────────────────────────────────────────────────────────

var (
	listenAddr   = flag.String("addr", ":8083", "FIX TCP listen address")
	bridgeCompID = flag.String("compid", "FXPBRIDGE", "Bridge SenderCompID in outbound FIX messages")
)

// ── Entry point ───────────────────────────────────────────────────────────────

func main() {
	logging.Setup()
	flag.Parse()

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
	defer func() { _ = registry.Close() }()

	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", *listenAddr, err)
	}
	defer listener.Close()

	log.Printf("🌉 FXP FIX Bridge listening on %s", *listenAddr)
	log.Printf("   SenderCompID: %s", *bridgeCompID)
	log.Printf("   Gateway:      127.0.0.1:8082")
	log.Printf("   FXP_JWT_SECRET: set ✓")
	log.Printf("   FIX CompIDs allowed: %d", len(creds))

	acceptFIXClients(listener, fixMaxConnections(), func(conn net.Conn) {
		log.Printf("📞 FIX client connected from %s", conn.RemoteAddr())
		handleFIXClient(conn, *bridgeCompID, jwtSecret, auth, registry)
	})
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
	if maxConnections <= 0 {
		maxConnections = 1
	}
	slots := make(chan struct{}, maxConnections)
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("accept error: %v", err)
			return
		}
		select {
		case slots <- struct{}{}:
			go func(conn net.Conn) {
				defer func() {
					_ = conn.Close()
					<-slots
				}()
				handle(conn)
			}(conn)
		default:
			log.Printf("FIX connection limit reached (%d); closing %s", maxConnections, conn.RemoteAddr())
			_ = conn.Close()
		}
	}
}

// ── Per-client handler ────────────────────────────────────────────────────────

// handleFIXClient runs the full lifecycle for one FIX client connection.
// Creates a session, a gateway client, wires them together, and blocks
// until the FIX session ends.
func handleFIXClient(conn net.Conn, bridgeCompID, jwtSecret string, auth session.Authenticator, registry *session.Registry) {
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
	sess.Auth = auth
	sess.Registry = registry
	h.sess = sess

	// Run the session (blocks until disconnect)
	sess.Run()
}

// ── Bridge handler ────────────────────────────────────────────────────────────

// bridgeHandler implements session.Handler. It connects to the FXP gateway
// on Logon, forwards application messages, and routes ExecutionReports back
// to the FIX client.
type bridgeHandler struct {
	bridgeCompID string
	jwtSecret    string
	sess         *session.Session
	gwClient     *gateway.Client
	pending      pendingRequests
	md           mdSubscriptions
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
	log.Printf("[%s] OnLogout: %q — closing gateway connection", sess.TargetCompID, reason)
	if h.gwClient != nil {
		h.gwClient.Close()
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
		h.sendBusinessReject(msg, sess, err.Error())
		return
	}

	// Recorded before sending so the snapshot cannot outrun it; undone on failure.
	var undoMD func()
	if mdReq := fxpMsg.GetMarketDataRequest(); mdReq != nil {
		stream := msg.Get(codec.TagSubscriptionRequestType) == "1"
		undoMD = h.md.subscribe(msg.Get(codec.TagMDReqID), stream, mdReq.Symbols)
	}

	req := fixRequestFor(fxpMsg)
	tracked := req != nil && isCancelOrReplace(req)
	if tracked {
		h.pending.add(req)
	}
	if err := h.gwClient.Send(fxpMsg); err != nil {
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
	codec.MsgTypeNewOrderSingle:            true,
	codec.MsgTypeOrderCancelRequest:        true,
	codec.MsgTypeOrderCancelReplaceRequest: true,
	codec.MsgTypeMarketDataRequest:         true,
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
	b.Set(codec.TagText, "Unsupported MsgType "+name+"; supported: D, F, G, V")
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
