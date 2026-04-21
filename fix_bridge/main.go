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
	"net"
	"os"
	"sync/atomic"

	"fix_bridge/codec"
	"fix_bridge/gateway"
	"fix_bridge/session"
	"fix_bridge/translator"

	"go_gateway/protobuf"
)

// ── Config ────────────────────────────────────────────────────────────────────

var (
	listenAddr  = flag.String("addr", ":8083", "FIX TCP listen address")
	bridgeCompID = flag.String("compid", "FXPBRIDGE", "Bridge SenderCompID in outbound FIX messages")
)

// ── Entry point ───────────────────────────────────────────────────────────────

func main() {
	flag.Parse()

	jwtSecret := os.Getenv("FXP_JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("FXP_JWT_SECRET environment variable not set")
	}

	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", *listenAddr, err)
	}
	defer listener.Close()

	log.Printf("🌉 FXP FIX Bridge listening on %s", *listenAddr)
	log.Printf("   SenderCompID: %s", *bridgeCompID)
	log.Printf("   Gateway:      127.0.0.1:8082")
	log.Printf("   FXP_JWT_SECRET: set ✓")

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("accept error: %v", err)
			continue
		}
		log.Printf("📞 FIX client connected from %s", conn.RemoteAddr())
		go handleFIXClient(conn, *bridgeCompID, jwtSecret)
	}
}

// ── Per-client handler ────────────────────────────────────────────────────────

// handleFIXClient runs the full lifecycle for one FIX client connection.
// Creates a session, a gateway client, wires them together, and blocks
// until the FIX session ends.
func handleFIXClient(conn net.Conn, bridgeCompID, jwtSecret string) {
	// The bridge handler implements session.Handler.
	// It holds references to both the session (to send FIX frames back)
	// and the gateway client (to forward orders).
	h := &bridgeHandler{
		bridgeCompID: bridgeCompID,
		jwtSecret:    jwtSecret,
	}

	// Create the FIX session — TargetCompID is filled in when Logon arrives
	sess := session.NewSession(conn, bridgeCompID, h)
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
	outSeqNum    int64 // FIX outbound sequence number for translated FXP messages
}

// nextSeq returns the next outbound FIX sequence number for gateway-originated messages.
// The session manages its own sequence numbers for session-level messages (Logon, Heartbeat).
// We use a separate counter here because the session's counter is managed internally.
func (h *bridgeHandler) nextSeq() int {
	return int(atomic.AddInt64(&h.outSeqNum, 1))
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
			// Send Logout to FIX client — gateway is unavailable
			h.sess.Send(codec.NewLogout(
				h.bridgeCompID, sess.TargetCompID, 1,
				fmt.Sprintf("gateway unavailable: %v", err),
			))
		}
	}()
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
	if h.gwClient == nil {
		log.Printf("[%s] received application message before gateway connected — dropping", sess.TargetCompID)
		return
	}

	fxpMsg, err := translator.TranslateFIXtoFXP(msg)
	if err != nil {
		log.Printf("[%s] translation error for %s: %v", sess.TargetCompID, msg.MsgType(), err)
		h.sendBusinessReject(msg, sess, err.Error())
		return
	}

	if err := h.gwClient.Send(fxpMsg); err != nil {
		log.Printf("[%s] gateway send error: %v", sess.TargetCompID, err)
	}
}

// onFXPMessage is called by the gateway client for every inbound FXP frame.
// Translates the FXP message to FIX and delivers to the FIX client.
func (h *bridgeHandler) onFXPMessage(msg *protobuf.FXPMessage) {
	seq := h.nextSeq()
	frame := translator.TranslateFXPtoFIX(msg, h.bridgeCompID, h.sess.TargetCompID, seq)
	if frame == nil {
		return // message type not translated (e.g. LogonAck)
	}
	h.sess.Send(frame)
}

// sendBusinessReject sends a FIX ExecutionReport with OrdStatus=Rejected
// when a NewOrderSingle cannot be translated.
// For cancel/replace failures, sends an OrderCancelReject.
func (h *bridgeHandler) sendBusinessReject(msg *codec.Message, sess *session.Session, reason string) {
	seq := h.nextSeq()

	switch msg.MsgType() {
	case codec.MsgTypeNewOrderSingle:
		b := codec.NewBuilder(codec.MsgTypeExecutionReport, h.bridgeCompID, sess.TargetCompID, seq)
		b.Set(codec.TagOrderID,   "NONE")
		b.Set(codec.TagExecID,    fmt.Sprintf("REJ-%s", msg.Get(codec.TagClOrdID)))
		b.Set(codec.TagExecType,  codec.ExecTypeRejected)
		b.Set(codec.TagOrdStatus, codec.OrdStatusRejected)
		b.Set(codec.TagClOrdID,   msg.Get(codec.TagClOrdID))
		b.Set(codec.TagSymbol,    msg.Get(codec.TagSymbol))
		b.SetInt(codec.TagLeavesQty, 0)
		b.SetInt(codec.TagCumQty,    0)
		b.Set(codec.TagOrdRejReason, codec.OrdRejReasonOther)
		b.Set(codec.TagText, reason)
		sess.Send(b.Build())

	case codec.MsgTypeOrderCancelRequest, codec.MsgTypeOrderCancelReplaceRequest:
		b := codec.NewBuilder(codec.MsgTypeOrderCancelReject, h.bridgeCompID, sess.TargetCompID, seq)
		b.Set(codec.TagOrderID,     "NONE")
		b.Set(codec.TagClOrdID,     msg.Get(codec.TagClOrdID))
		b.Set(codec.TagOrigClOrdID, msg.Get(codec.TagOrigClOrdID))
		b.Set(codec.TagOrdStatus,   codec.OrdStatusRejected)
		b.Set(codec.TagCxlRejReason, "0") // Too late to cancel
		if msg.MsgType() == codec.MsgTypeOrderCancelReplaceRequest {
			b.Set(codec.TagCxlRejResponseTo, "2")
		} else {
			b.Set(codec.TagCxlRejResponseTo, "1")
		}
		b.Set(codec.TagText, reason)
		sess.Send(b.Build())

	default:
		// For other message types, just log — no standard rejection message
		log.Printf("[%s] cannot send business reject for %s: %s", sess.TargetCompID, msg.MsgType(), reason)
	}
}