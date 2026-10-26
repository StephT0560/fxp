// SPDX-License-Identifier: MIT

package transport

// pool.go — Connection pool for FXP Core.
//
// Replaces the single `conn` + `connLock` pattern that serialised all
// order writes behind one mutex, causing latency to spike 1700x under
// concurrency=10 and complete timeout at concurrency=50.
//
// Design:
//   - Fixed pool of N persistent connections to FXP Core (default: NumCPU)
//   - Each connection has its own dedicated reader goroutine
//   - Senders acquire a connection, write, release — no global lock
//   - ExecutionReport routing uses the existing orderClientMap (unchanged)
//   - Market data fan-out uses the existing ForwardMarketData (unchanged)
//   - Automatic reconnection on write failure; the connection's single
//     reader goroutine resumes on the new socket

import (
	"encoding/binary"
	"errors"
	"fmt"
	"go_gateway/diagnostics"
	"io"
	"log"
	"log/slog"
	"net"
	"runtime"
	"sync"
	"time"

	"go_gateway/coreauth"
	"go_gateway/protobuf"
	"os"

	"google.golang.org/protobuf/proto"
)

// poolSize: one connection per logical CPU. Tunable via SetPoolSize().
var poolSize = runtime.NumCPU()

func SetPoolSize(n int) { poolSize = n }

// ── pooledConn ────────────────────────────────────────────────────────────────

// errConnDown is returned by write when the reader has marked the connection
// dead; the caller reconnects.
var errConnDown = errors.New("pool connection is down")

// pooledConn is one core connection. Writers share it under mu; exactly one
// poolReader goroutine owns its read side for the pool's lifetime. reconnect
// swaps conn and wakes that reader instead of starting another one.
type pooledConn struct {
	// md marks the dedicated market-data connection (see mdConn).
	md      bool
	mu      sync.Mutex
	changed *sync.Cond // signalled when conn is replaced or the pool shuts down
	conn    net.Conn
	id      int
	closed  bool
}

// coreAddr is a variable so tests can point the pool at a fake core.
var coreAddr = "127.0.0.1:8080"

// dialCore opens a core connection with Nagle disabled. Go already defaults
// TCP_NODELAY to on; it is set explicitly so the latency contract is visible
// and covered by TestDialCoreSetsNoDelay.
func dialCore(addr string) (net.Conn, error) {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	if err := c.(*net.TCPConn).SetNoDelay(true); err != nil {
		_ = c.Close()
		return nil, err
	}
	if err := coreauth.Authenticate(c, os.Getenv("FXP_CORE_AUTH_TOKEN")); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func newPooledConnWith(id int, c net.Conn) *pooledConn {
	p := &pooledConn{conn: c, id: id}
	p.changed = sync.NewCond(&p.mu)
	return p
}

func newPooledConn(id int) (*pooledConn, error) {
	c, err := dialCore(coreAddr)
	if err != nil {
		return nil, err
	}
	return newPooledConnWith(id, c), nil
}

// write sends bytes to FXP Core under the per-connection lock.
func (p *pooledConn) write(frame []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return errConnDown
	}
	diagnostics.Raw("gateway_core_write", frame[4:])
	_, err := p.conn.Write(frame)
	return err
}

// reconnect replaces the underlying connection and hands it to the reader.
func (p *pooledConn) reconnect() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return net.ErrClosed
	}
	if p.conn != nil {
		p.conn.Close()
		p.conn = nil
	}
	c, err := dialCore(coreAddr)
	if err != nil {
		return err
	}
	p.conn = c
	p.changed.Broadcast()
	log.Printf("🔄 Pool conn %d reconnected", p.id)
	return nil
}

// redial dials only if the connection is down, so it never replaces a
// connection a concurrent writer already re-established.
func (p *pooledConn) redial() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return net.ErrClosed
	}
	if p.conn != nil {
		return nil
	}
	c, err := dialCore(coreAddr)
	if err != nil {
		return err
	}
	p.conn = c
	p.changed.Broadcast()
	return nil
}

// restoreMarketData reconnects the market-data connection without waiting
// for a write and replays every active subscription: the core forgets
// subscribers whose connection closed, and synced clients never re-request.
func restoreMarketData(pc *pooledConn) {
	delay := 100 * time.Millisecond
	for {
		err := pc.redial()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err == nil {
			if err = replaySubscriptions(pc); err == nil {
				log.Printf("🔄 Market-data subscriptions replayed on pool conn %d", pc.id)
				return
			}
		}
		log.Printf("⚠️ Market-data restore on pool conn %d: %v", pc.id, err)
		time.Sleep(delay)
		delay = min(2*delay, 5*time.Second)
	}
}

// nextConn blocks until a live connection is available, or returns nil once
// the pool connection is shut down.
func (p *pooledConn) nextConn() net.Conn {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.conn == nil && !p.closed {
		p.changed.Wait()
	}
	if p.closed {
		return nil
	}
	return p.conn
}

// markDead closes c and clears it if it is still the current connection, so
// the next write reconnects. A conn already replaced by reconnect is left alone.
func (p *pooledConn) markDead(c net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = c.Close()
	if p.conn == c {
		p.conn = nil
	}
}

// shutdown closes the connection and stops its reader.
func (p *pooledConn) shutdown() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
	p.changed.Broadcast()
}

// ── pool ──────────────────────────────────────────────────────────────────────

var (
	corePool chan *pooledConn
	poolOnce sync.Once
	// mdConn carries every MarketDataRequest. The core registers the sending
	// connection as the symbol's subscriber, so using one connection keeps the
	// gateway a single subscriber and fan-out free of duplicates.
	mdConn *pooledConn
)

// InitPool creates poolSize connections to FXP Core and starts one reader
// goroutine per connection. Safe to call multiple times — only runs once.
func InitPool() error {
	var initErr error
	poolOnce.Do(func() {
		log.Printf("🔌 Initialising FXP Core connection pool (size=%d)...", poolSize)
		corePool = make(chan *pooledConn, poolSize)

		for i := 0; i < poolSize; i++ {
			pc, err := newPooledConn(i)
			if err != nil {
				initErr = err
				return
			}
			if i == 0 {
				pc.md = true
				mdConn = pc
			}
			corePool <- pc
			go poolReader(pc)
			log.Printf("  ✅ Pool connection %d ready", i+1)
		}
		log.Printf("✅ Connection pool ready (%d connections to FXP Core)", poolSize)
	})
	return initErr
}

func ShutdownPool() {
	if corePool == nil {
		return
	}
	for range poolSize {
		select {
		case connection := <-corePool:
			connection.shutdown()
		default:
			log.Printf("core connection pool shutdown left in-use connections")
			return
		}
	}
}

// acquire blocks until a pool connection is available.
func acquire() *pooledConn { return <-corePool }

// release returns a connection to the pool.
func release(pc *pooledConn) { corePool <- pc }

// ── SendFramedToCore ─────────────────────────────────────────────────────────

// SendFramedToCore sends a pre-framed FXP message to Rust Core via the pool.
// orderID is registered in orderClientMap BEFORE acquiring the pool slot so
// the reader goroutine can always route the ExecutionReport back correctly.
// provisional is true for cancel/replace requests (see registerRoute).
func SendFramedToCore(clientConn any, frame []byte, orderID string, owner string, provisional bool) {
	// Register before acquiring pool slot — avoids a race where the
	// ExecutionReport arrives before the map entry is written.
	var stored bool
	if provisional {
		stored = registerRequest(orderID, clientConn, owner)
	} else {
		stored = registerRoute(orderID, orderRoute{conn: clientConn, owner: owner})
	}

	if err := writeToCore(frame); err != nil {
		log.Printf("❌ OrderID %s not sent to core: %v", orderID, err)
		if stored {
			orderClientMap.Delete(orderID)
		}
		inflightRequests.Delete(orderID)
		return
	}
	slog.Debug("order sent via pool", "order_id", orderID)
}

// writeToCore writes one frame on a pooled connection, reconnecting and
// retrying once if the write fails.
func writeToCore(frame []byte) error {
	pc := acquire()
	defer release(pc)
	return writeVia(pc, frame)
}

func writeVia(pc *pooledConn, frame []byte) error {
	if err := pc.write(frame); err != nil {
		log.Printf("❌ Pool conn %d write failed: %v — reconnecting", pc.id, err)
		if rerr := pc.reconnect(); rerr != nil {
			return fmt.Errorf("pool conn %d reconnect: %w", pc.id, rerr)
		}
		// The connection's existing poolReader picks up the new conn.
		if err := pc.write(frame); err != nil {
			return fmt.Errorf("pool conn %d retry: %w", pc.id, err)
		}
	}
	return nil
}

func frameBytes(raw []byte) []byte {
	frame := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(raw)))
	copy(frame[4:], raw)
	return frame
}

// ── poolReader ────────────────────────────────────────────────────────────────

// poolReader is the only reader of one pool connection, for its whole life.
// It routes ExecutionReports and market data updates from FXP Core. On any
// read or framing error it drops the connection (the stream can no longer be
// trusted to be frame-aligned) and waits for reconnect to supply a new one.
func poolReader(pc *pooledConn) {
	log.Printf("🧵 Pool reader %d started", pc.id)
	for {
		c := pc.nextConn()
		if c == nil {
			log.Printf("🧵 Pool reader %d stopped", pc.id)
			return
		}
		err := readCoreFrames(c)
		log.Printf("⚠️ Pool reader %d: %v — waiting for reconnect", pc.id, err)
		pc.markDead(c)
		if pc.md {
			go restoreMarketData(pc)
		}
	}
}

// readCoreFrames reads frames from c until an error, which it returns.
func readCoreFrames(c net.Conn) error {
	hdr := make([]byte, 4)
	for {
		if _, err := io.ReadFull(c, hdr); err != nil {
			return fmt.Errorf("read error: %w", err)
		}
		msgLen := int(binary.BigEndian.Uint32(hdr))
		if msgLen <= 0 || msgLen > 1_048_576 {
			return fmt.Errorf("invalid frame length %d", msgLen)
		}
		body := make([]byte, msgLen)
		if _, err := io.ReadFull(c, body); err != nil {
			return fmt.Errorf("body read error: %w", err)
		}
		msg := &protobuf.FXPMessage{}
		if err := proto.Unmarshal(body, msg); err != nil {
			// The frame boundary is intact, so skip just this message.
			log.Printf("⚠️ Pool reader: decode error: %v", err)
			continue
		}
		diagnostics.Message("gateway_core_receive", msg)
		onCoreMessage(msg)
	}
}

// onCoreMessage handles every frame read from the core; tests replace it.
var onCoreMessage = routeFromCore

// ── routeFromCore ─────────────────────────────────────────────────────────────

// routeFromCore dispatches messages received from FXP Core.
// Called from all pool reader goroutines — uses sync.Map so no lock needed.
func routeFromCore(msg *protobuf.FXPMessage) {
	switch payload := msg.Payload.(type) {
	case *protobuf.FXPMessage_OrderStatusResponse:
		routeStatusResponse(msg)

	case *protobuf.FXPMessage_ExecutionReport:
		rep := payload.ExecutionReport
		orderID := rep.OrderId
		status := protobuf.OrderStatus(rep.OrderStatus)
		slog.Debug("ExecutionReport received", "order_id", orderID, "status", status)

		routeAny, ok := orderClientMap.Load(orderID)
		if !ok {
			log.Printf("⚠️ No registered client for OrderID %s", orderID)
			return
		}
		route := routeAny.(orderRoute)
		clientAny := route.conn
		// The core marks rejects of cancel/replace requests with orig_order_id:
		// the targeted order itself is unchanged.
		requestReject := status == protobuf.OrderStatus_ORDER_REJECTED && rep.OrigOrderId != ""
		stripOwner(rep, route.owner)

		encoded, _ := proto.Marshal(msg)
		targets := []any{clientAny}
		isRequestReply := requestReject || status == protobuf.OrderStatus_ORDER_REPLACED ||
			(status == protobuf.OrderStatus_ORDER_CANCELED && rep.LeavesQuantity == 0)
		if isRequestReply {
			if req, ok := inflightRequests.LoadAndDelete(orderID); ok {
				if requestReject {
					targets = []any{req} // only the requester asked
				} else {
					targets = append(targets, req)
				}
			}
		}
		for _, c := range targets {
			deliverReport(c, encoded, orderID)
		}

		// Remove from orderClientMap only when the order is truly done.
		//
		// ORDER_CANCELED with leaves_quantity > 0 means an IOC/FOK remainder
		// cancel — more reports (the partial fill) will follow on the same
		// orderID. Keep the map entry alive so they can be delivered.
		//
		// ORDER_CANCELED with leaves_quantity == 0 means a full cancel (resting
		// limit cancelled, or no fill at all) — safe to remove.
		if requestReject {
			if route.provisional {
				orderClientMap.Delete(orderID)
			}
			if newID, ok := pendingReplace.LoadAndDelete(orderID); ok {
				dropProvisional(newID.(string))
			}
			return
		}
		if status == protobuf.OrderStatus_ORDER_REPLACED {
			pendingReplace.Delete(orderID)
		}

		isCanceled := status == protobuf.OrderStatus_ORDER_CANCELED
		fullCancel := isCanceled && rep.LeavesQuantity == 0

		terminal := status == protobuf.OrderStatus_ORDER_FILLED ||
			status == protobuf.OrderStatus_ORDER_REJECTED ||
			status == protobuf.OrderStatus_ORDER_REPLACED ||
			status == protobuf.OrderStatus_ORDER_EXPIRED ||
			fullCancel

		if terminal {
			orderClientMap.Delete(orderID)
			slog.Debug("order removed from client map (terminal)", "order_id", orderID)
		} else if route.provisional {
			route.provisional = false
			orderClientMap.Store(orderID, route)
		}
		if !terminal && isCanceled {
			slog.Debug("cancel with leaves>0; keeping order in client map", "order_id", orderID)
		}

	case *protobuf.FXPMessage_OrderMassCancelReport:
		routeMassCancelResponse(msg)

	case *protobuf.FXPMessage_MarketDataIncremental:
		slog.Debug("MarketDataIncrementalUpdate received", "symbol", payload.MarketDataIncremental.Symbol)
		ForwardMarketData(payload.MarketDataIncremental)

	case *protobuf.FXPMessage_MarketDataSnapshot:
		slog.Debug("MarketDataSnapshot received", "symbol", payload.MarketDataSnapshot.Symbol)
		ForwardMarketData(payload.MarketDataSnapshot)

	case *protobuf.FXPMessage_TradeCaptureReport:
		slog.Debug("TradeCaptureReport received", "symbol", payload.TradeCaptureReport.Symbol)
		ForwardMarketData(payload.TradeCaptureReport)

	case *protobuf.FXPMessage_TradingSessionStatus:
		ForwardMarketData(payload.TradingSessionStatus)

	case *protobuf.FXPMessage_AuctionInfo:
		ForwardMarketData(payload.AuctionInfo)

	default:
		log.Printf("⚠️ Pool reader: unhandled payload type: %T", msg.Payload)
	}
}

// deliverReport writes an encoded ExecutionReport to a TCP or WebSocket client.
func deliverReport(clientAny any, encoded []byte, orderID string) {
	if sendErr := enqueueClientData(clientAny, encoded, false); sendErr != nil {
		log.Printf("❌ ExecutionReport delivery failed for OrderID %s: %v", orderID, sendErr)
	} else {
		slog.Debug("ExecutionReport delivered", "order_id", orderID)
	}
}
