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
//   - Automatic reconnection on write failure

import (
	"encoding/binary"
	"io"
	"log"
	"net"
	"runtime"
	"sync"
	"time"

	"go_gateway/protobuf"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

const rustCoreAddr = "127.0.0.1:8080"

// poolSize: one connection per logical CPU. Tunable via SetPoolSize().
var poolSize = runtime.NumCPU()

func SetPoolSize(n int) { poolSize = n }

// ── pooledConn ────────────────────────────────────────────────────────────────

type pooledConn struct {
	mu   sync.Mutex
	conn net.Conn
	id   int
}

func newPooledConn(id int) (*pooledConn, error) {
	c, err := net.Dial("tcp", rustCoreAddr)
	if err != nil {
		return nil, err
	}
	return &pooledConn{conn: c, id: id}, nil
}

// write sends bytes to FXP Core under the per-connection lock.
func (p *pooledConn) write(frame []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.conn.Write(frame)
	return err
}

// reconnect replaces the underlying connection.
func (p *pooledConn) reconnect() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		p.conn.Close()
		p.conn = nil
	}
	c, err := net.Dial("tcp", rustCoreAddr)
	if err != nil {
		return err
	}
	p.conn = c
	log.Printf("🔄 Pool conn %d reconnected", p.id)
	return nil
}

// rawConn returns the underlying net.Conn (used by the reader goroutine).
func (p *pooledConn) rawConn() net.Conn {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conn
}

// ── pool ──────────────────────────────────────────────────────────────────────

var (
	corePool chan *pooledConn
	poolOnce sync.Once
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
			corePool <- pc
			go poolReader(pc)
			log.Printf("  ✅ Pool connection %d ready", i+1)
		}
		log.Printf("✅ Connection pool ready (%d connections to FXP Core)", poolSize)
	})
	return initErr
}

// acquire blocks until a pool connection is available.
func acquire() *pooledConn { return <-corePool }

// release returns a connection to the pool.
func release(pc *pooledConn) { corePool <- pc }

// ── SendFramedToCore ─────────────────────────────────────────────────────────

// SendFramedToCore sends a pre-framed FXP message to Rust Core via the pool.
// orderID is registered in orderClientMap BEFORE acquiring the pool slot so
// the reader goroutine can always route the ExecutionReport back correctly.
func SendFramedToCore(clientConn any, frame []byte, orderID string) {
	// Register before acquiring pool slot — avoids a race where the
	// ExecutionReport arrives before the map entry is written.
	orderClientMap.Store(orderID, clientConn)

	pc := acquire()
	defer release(pc)

	if err := pc.write(frame); err != nil {
		log.Printf("❌ Pool conn %d write failed (OrderID %s): %v — reconnecting", pc.id, orderID, err)
		if rerr := pc.reconnect(); rerr != nil {
			log.Printf("❌ Pool conn %d reconnect failed: %v", pc.id, rerr)
			orderClientMap.Delete(orderID)
			return
		}
		// Restart reader on the fresh connection
		go poolReader(pc)
		// Retry once
		if err = pc.write(frame); err != nil {
			log.Printf("❌ Pool conn %d retry failed (OrderID %s): %v", pc.id, orderID, err)
			orderClientMap.Delete(orderID)
			return
		}
	}
	log.Printf("📤 OrderID %s sent via pool conn %d", orderID, pc.id)
}

// ── poolReader ────────────────────────────────────────────────────────────────

// poolReader is the dedicated reader goroutine for one pool connection.
// It routes ExecutionReports and market data updates from FXP Core.
func poolReader(pc *pooledConn) {
	log.Printf("🧵 Pool reader %d started", pc.id)
	hdr := make([]byte, 4)

	for {
		c := pc.rawConn()
		if c == nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}

		if _, err := io.ReadFull(c, hdr); err != nil {
			log.Printf("⚠️ Pool reader %d: read error: %v", pc.id, err)
			// Mark dead so the next SendFramedToCore call will reconnect.
			pc.mu.Lock()
			if pc.conn == c {
				pc.conn = nil
			}
			pc.mu.Unlock()
			// Wait until SendFramedToCore reconnects, then restart reading.
			for pc.rawConn() == nil {
				time.Sleep(50 * time.Millisecond)
			}
			log.Printf("🧵 Pool reader %d resuming after reconnect", pc.id)
			continue
		}

		msgLen := int(binary.BigEndian.Uint32(hdr))
		if msgLen <= 0 || msgLen > 1_048_576 {
			log.Printf("⚠️ Pool reader %d: invalid frame length %d", pc.id, msgLen)
			continue
		}

		body := make([]byte, msgLen)
		if _, err := io.ReadFull(c, body); err != nil {
			log.Printf("⚠️ Pool reader %d: body read error: %v", pc.id, err)
			continue
		}

		msg := &protobuf.FXPMessage{}
		if err := proto.Unmarshal(body, msg); err != nil {
			log.Printf("⚠️ Pool reader %d: decode error: %v", pc.id, err)
			continue
		}

		routeFromCore(msg)
	}
}

// ── routeFromCore ─────────────────────────────────────────────────────────────

// routeFromCore dispatches messages received from FXP Core.
// Called from all pool reader goroutines — uses sync.Map so no lock needed.
func routeFromCore(msg *protobuf.FXPMessage) {
	switch payload := msg.Payload.(type) {

	case *protobuf.FXPMessage_ExecutionReport:
		rep := payload.ExecutionReport
		orderID := rep.OrderId
		status := protobuf.OrderStatus(rep.OrderStatus)
		log.Printf("📩 ExecutionReport for OrderID %s status=%s", orderID, status)

		clientAny, ok := orderClientMap.Load(orderID)
		if !ok {
			log.Printf("⚠️ No registered client for OrderID %s", orderID)
			return
		}

		encoded, _ := proto.Marshal(msg)
		var sendErr error

		switch client := clientAny.(type) {
		case net.Conn:
			frame := make([]byte, 4+len(encoded))
			binary.BigEndian.PutUint32(frame[:4], uint32(len(encoded)))
			copy(frame[4:], encoded)
			_, sendErr = client.Write(frame)
		case *websocket.Conn:
			sendErr = client.WriteMessage(websocket.BinaryMessage, encoded)
		default:
			log.Printf("⚠️ Unknown client type for OrderID %s: %T", orderID, clientAny)
			return
		}

		if sendErr != nil {
			log.Printf("❌ ExecutionReport delivery failed for OrderID %s: %v", orderID, sendErr)
		} else {
			log.Printf("✅ ExecutionReport delivered for OrderID %s", orderID)
		}

		// Remove from orderClientMap only when the order is truly done.
		//
		// ORDER_CANCELED with leaves_quantity > 0 means an IOC/FOK remainder
		// cancel — more reports (the partial fill) will follow on the same
		// orderID. Keep the map entry alive so they can be delivered.
		//
		// ORDER_CANCELED with leaves_quantity == 0 means a full cancel (resting
		// limit cancelled, or no fill at all) — safe to remove.
		isCanceled := status == protobuf.OrderStatus_ORDER_CANCELED
		fullCancel := isCanceled && rep.LeavesQuantity == 0

		terminal := status == protobuf.OrderStatus_ORDER_FILLED ||
			status == protobuf.OrderStatus_ORDER_REJECTED ||
			status == protobuf.OrderStatus_ORDER_REPLACED ||
			status == protobuf.OrderStatus_ORDER_EXPIRED ||
			fullCancel

		if terminal {
			orderClientMap.Delete(orderID)
			log.Printf("🗑️ OrderID %s removed from client map (terminal)", orderID)
		} else if isCanceled {
			log.Printf("📬 OrderID %s cancel with leaves>0 — keeping in map for partial fill", orderID)
		}

	case *protobuf.FXPMessage_MarketDataIncremental:
		log.Printf("📡 MarketDataIncrementalUpdate for symbol: %s", payload.MarketDataIncremental.Symbol)
		ForwardMarketData(payload.MarketDataIncremental)

	case *protobuf.FXPMessage_TradeCaptureReport:
		log.Printf("📡 TradeCaptureReport for symbol: %s", payload.TradeCaptureReport.Symbol)
		ForwardMarketData(payload.TradeCaptureReport)

	default:
		log.Printf("⚠️ Pool reader: unhandled payload type: %T", msg.Payload)
	}
}