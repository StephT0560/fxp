// trading_day.go — FXP Protocol Trading Day Simulation
//
// Simulates a realistic institutional trading day with multiple session phases,
// diverse order types, and configurable participant behaviour.
//
// Session phases:
//   PRE_MARKET  (  0 –  9:29) Orders accepted, no matching
//   OPEN_AUCTION( 9:29 –  9:30) AT_OPEN orders batch-matched at open
//   CONTINUOUS  ( 9:30 – 15:55) Normal limit/market/IOC/FOK/stop matching
//   CLOSE_AUCTION(15:55 – 16:00) AT_CLOSE / MOC orders batch-matched at close
//   CLOSED      (16:00+        ) No new orders accepted
//
// Participant types:
//   MarketMaker  — posts tight bid/ask spreads, cancels and re-quotes aggressively
//   Institutional — large limit orders, some AT_OPEN/AT_CLOSE, occasional stops
//   Retail       — small market and limit orders, random timing
//   HedgeFund    — momentum-driven, uses stop orders to manage risk
//
// Usage:
//   go run simulation/trading_day.go
//   go run simulation/trading_day.go --symbols 5 --duration 60 --participants 20
//   go run simulation/trading_day.go --symbols 10 --duration 300 --seed 42
//
// Prerequisites:
//   cargo run  (fxp_core, terminal 1)
//   go run main.go  (go_gateway, terminal 2)
//   export FXP_JWT_SECRET="dev-secret-change-in-production"

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"go_gateway/protobuf"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ── Config ────────────────────────────────────────────────────────────────────

const (
	gatewayAddr = "127.0.0.1:8082"
	jwtSecret   = "dev-secret-change-in-production"
)

var (
	numSymbols   = flag.Int("symbols", 5, "Number of symbols to trade")
	numParticipants = flag.Int("participants", 20, "Number of trading participants")
	durationSec  = flag.Int("duration", 120, "Simulation wall-clock duration in seconds")
	seed         = flag.Int64("seed", 0, "Random seed (0 = use current time)")
	verbose      = flag.Bool("verbose", false, "Print individual order events")
)

// ── Session phases ────────────────────────────────────────────────────────────

type Phase int

const (
	PhasePreMarket Phase = iota
	PhaseOpenAuction
	PhaseContinuous
	PhaseCloseAuction
	PhaseClosed
)

func (p Phase) String() string {
	switch p {
	case PhasePreMarket:   return "PRE_MARKET"
	case PhaseOpenAuction: return "OPEN_AUCTION"
	case PhaseContinuous:  return "CONTINUOUS"
	case PhaseCloseAuction: return "CLOSE_AUCTION"
	case PhaseClosed:      return "CLOSED"
	default:               return "UNKNOWN"
	}
}

// ── Symbol state ──────────────────────────────────────────────────────────────

type SymbolState struct {
	mu           sync.Mutex
	symbol       string
	midPrice     int64  // fixed-point cents, e.g. 15000 = $150.00
	volatility   int64  // typical price move per step, in cents
	tickSize     int64  // minimum price increment, in cents
	openPrice    int64
	lastPrice    int64
	highPrice    int64
	lowPrice     int64
	volume       int64
	activeOrders map[string]string // orderID → side (buy/sell)
}

func newSymbolState(symbol string, midPrice int64, volatility int64) *SymbolState {
	return &SymbolState{
		symbol:       symbol,
		midPrice:     midPrice,
		volatility:   volatility,
		tickSize:     1, // 1 cent
		openPrice:    midPrice,
		lastPrice:    midPrice,
		highPrice:    midPrice,
		lowPrice:     midPrice,
		activeOrders: make(map[string]string),
	}
}

// nextPrice simulates a small random price move (geometric Brownian motion approximation)
func (s *SymbolState) nextPrice(rng *rand.Rand) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Random walk: ±volatility with slight mean-reversion
	move := int64(rng.NormFloat64() * float64(s.volatility))
	// Mean reversion: pull back 5% toward open price
	reversion := (s.openPrice - s.midPrice) / 20
	s.midPrice += move + reversion
	// Floor at 1 cent
	if s.midPrice < 1 {
		s.midPrice = 1
	}
	return s.midPrice
}

func (s *SymbolState) recordFill(price int64, qty int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastPrice = price
	s.volume += qty
	if price > s.highPrice { s.highPrice = price }
	if price < s.lowPrice  { s.lowPrice  = price }
}

// ── Participant types ─────────────────────────────────────────────────────────

type ParticipantType int

const (
	TypeMarketMaker  ParticipantType = iota
	TypeInstitutional
	TypeRetail
	TypeHedgeFund
)

func (t ParticipantType) String() string {
	switch t {
	case TypeMarketMaker:   return "MarketMaker"
	case TypeInstitutional: return "Institutional"
	case TypeRetail:        return "Retail"
	case TypeHedgeFund:     return "HedgeFund"
	default:                return "Unknown"
	}
}

// ── Simulation stats ──────────────────────────────────────────────────────────

type SimStats struct {
	ordersPlaced    int64
	ordersFilled    int64
	ordersCancelled int64
	ordersRejected  int64
	stopsFired      int64
	cancelReplaces  int64
	marketOrders    int64
	limitOrders     int64
	stopOrders      int64
	iocOrders       int64
	atOpenOrders    int64
	atCloseOrders   int64
	latencies       []time.Duration
	mu              sync.Mutex
}

func (s *SimStats) recordLatency(d time.Duration) {
	s.mu.Lock()
	s.latencies = append(s.latencies, d)
	s.mu.Unlock()
}

func (s *SimStats) percentile(p float64) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.latencies) == 0 { return 0 }
	sorted := make([]time.Duration, len(s.latencies))
	copy(sorted, s.latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(math.Ceil(p/100.0*float64(len(sorted)))) - 1
	if idx < 0 { idx = 0 }
	if idx >= len(sorted) { idx = len(sorted) - 1 }
	return sorted[idx]
}

// ── JWT / connection helpers (self-contained) ─────────────────────────────────

func mustJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil { panic(err) }
	return b
}

func generateJWT(secret, traderID string) string {
	header  := base64.RawURLEncoding.EncodeToString(mustJSON(map[string]string{"alg": "HS256", "typ": "JWT"}))
	payload := base64.RawURLEncoding.EncodeToString(mustJSON(map[string]interface{}{
		"sub": traderID,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(24 * time.Hour).Unix(),
	}))
	unsigned := header + "." + payload
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(unsigned))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return unsigned + "." + sig
}

func connectAndLogon(traderID, sessionID string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", gatewayAddr, 5*time.Second)
	if err != nil { return nil, err }

	logon := &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_Logon{
			Logon: &protobuf.Logon{
				Token:     generateJWT(jwtSecret, traderID),
				SessionId: sessionID,
				Timestamp: timestamppb.Now(),
			},
		},
	}
	if err := sendFramed(conn, logon); err != nil {
		conn.Close()
		return nil, fmt.Errorf("logon send failed: %w", err)
	}

	// Read LogonAck
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	ack, err := readFramed(conn)
	conn.SetReadDeadline(time.Time{})
	if err != nil { conn.Close(); return nil, fmt.Errorf("logon ack failed: %w", err) }
	if la, ok := ack.Payload.(*protobuf.FXPMessage_LogonAck); ok && !la.LogonAck.Accepted {
		conn.Close()
		return nil, fmt.Errorf("logon rejected: %s", la.LogonAck.RejectReason)
	}
	return conn, nil
}

func sendFramed(conn net.Conn, msg *protobuf.FXPMessage) error {
	raw, err := proto.Marshal(msg)
	if err != nil { return err }
	frame := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(raw)))
	copy(frame[4:], raw)
	_, err = conn.Write(frame)
	return err
}

func readFramed(conn net.Conn) (*protobuf.FXPMessage, error) {
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBuf); err != nil { return nil, err }
	msgLen := binary.BigEndian.Uint32(lenBuf)
	if msgLen == 0 || msgLen > 1_048_576 {
		return nil, fmt.Errorf("invalid frame length: %d", msgLen)
	}
	body := make([]byte, msgLen)
	if _, err := io.ReadFull(conn, body); err != nil { return nil, err }
	msg := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(body, msg); err != nil { return nil, err }
	return msg, nil
}

// ── Participant ───────────────────────────────────────────────────────────────

type Participant struct {
	id            int
	ptype         ParticipantType
	traderID      string
	conn          net.Conn
	symbols       []*SymbolState
	symbolByOrder map[string]*SymbolState // orderID → symbol (for recordFill)
	stopOrderIDs  map[string]bool          // orderID → true for stop/stoplimit orders
	rng           *rand.Rand
	stats         *SimStats
	phase         *Phase
	phaseMu       *sync.RWMutex
	orderSeq      int
	pendingMu     sync.Mutex
	pending       map[string]time.Time // orderID → sent time (latency tracking)
	activeOrders  []string             // resting order IDs eligible for cancel
}

func (p *Participant) nextOrderID() string {
	p.orderSeq++
	return fmt.Sprintf("%s-%06d", p.traderID, p.orderSeq)
}

// orderRate returns orders per second for this participant type and phase
func (p *Participant) orderRate() float64 {
	base := map[ParticipantType]float64{
		TypeMarketMaker:   8.0,
		TypeInstitutional: 1.0,
		TypeRetail:        0.5,
		TypeHedgeFund:     2.0,
	}[p.ptype]

	p.phaseMu.RLock()
	ph := *p.phase
	p.phaseMu.RUnlock()

	switch ph {
	case PhaseOpenAuction:  return base * 3.0  // burst at open
	case PhaseContinuous:   return base
	case PhaseCloseAuction: return base * 2.0  // burst at close
	default:                return base * 0.1  // minimal in pre/post
	}
}

// run is the main loop for a participant goroutine
func (p *Participant) run(wg *sync.WaitGroup, done <-chan struct{}) {
	defer wg.Done()

	// Start reader goroutine
	go p.readLoop()

	for {
		select {
		case <-done:
			return
		default:
		}

		rate := p.orderRate()
		if rate <= 0 {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		interval := time.Duration(float64(time.Second) / rate)
		// Add jitter (±30%) to avoid lockstep behaviour
		jitter := time.Duration(p.rng.Float64()*0.6*float64(interval) - 0.3*float64(interval))
		time.Sleep(interval + jitter)

		p.phaseMu.RLock()
		ph := *p.phase
		p.phaseMu.RUnlock()

		p.sendOrder(ph)
	}
}

func (p *Participant) sendOrder(ph Phase) {
	sym := p.symbols[p.rng.Intn(len(p.symbols))]

	switch p.ptype {
	case TypeMarketMaker:
		p.sendMarketMakerOrder(sym, ph)
	case TypeInstitutional:
		p.sendInstitutionalOrder(sym, ph)
	case TypeRetail:
		p.sendRetailOrder(sym, ph)
	case TypeHedgeFund:
		p.sendHedgeFundOrder(sym, ph)
	}
}

func (p *Participant) sendMarketMakerOrder(sym *SymbolState, ph Phase) {
	mid := sym.nextPrice(p.rng)
	spread := sym.tickSize * int64(1+p.rng.Intn(3)) // 1–3 tick spread

	// Cancel a random old resting order first (re-quoting)
	p.maybeCancelOldOrder()

	// 15% chance: send an aggressive IOC to generate fills (cross the spread)
	if p.rng.Float64() < 0.15 {
		aggrSide := protobuf.OrderSide_BUY
		aggrPrice := mid + sym.volatility*2 // cross the ask
		if p.rng.Float64() < 0.5 {
			aggrSide = protobuf.OrderSide_SELL
			aggrPrice = mid - sym.volatility*2 // cross the bid
		}
		if aggrPrice < 1 { aggrPrice = 1 }
		aggrID := p.nextOrderID()
		aggrMsg := &protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_NewOrder{
				NewOrder: &protobuf.NewOrderSingle{
					Header:      &protobuf.FXPHeader{ProtocolVersion: "1.1", SequenceNumber: uint64(p.orderSeq)},
					OrderId:     aggrID, Sender: p.traderID, Receiver: "FXPExchange",
					Symbol:      sym.symbol, Price: aggrPrice, Side: aggrSide,
					Quantity:    int32(5 + p.rng.Intn(20)),
					OrderType:   protobuf.OrderType_LIMIT,
					TimeInForce: protobuf.TimeInForce_IOC,
					Timestamp:   timestamppb.Now(),
				},
			},
		}
		p.recordAndSendWithSymbol(aggrID, aggrMsg, sym)
		atomic.AddInt64(&p.stats.iocOrders, 1)
	}

	// Post bid and ask around mid
	for _, side := range []protobuf.OrderSide{protobuf.OrderSide_BUY, protobuf.OrderSide_SELL} {
		var price int64
		if side == protobuf.OrderSide_BUY {
			price = mid - spread/2
		} else {
			price = mid + spread/2
		}
		if price < sym.tickSize { price = sym.tickSize }

		qty := int32(10 + p.rng.Intn(90)) // 10–100 shares
		orderID := p.nextOrderID()

		msg := &protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_NewOrder{
				NewOrder: &protobuf.NewOrderSingle{
					Header:      &protobuf.FXPHeader{ProtocolVersion: "1.1", SequenceNumber: uint64(p.orderSeq)},
					OrderId:     orderID,
					Sender:      p.traderID,
					Receiver:    "FXPExchange",
					Symbol:      sym.symbol,
					Price:       price,
					Side:        side,
					Quantity:    qty,
					OrderType:   protobuf.OrderType_LIMIT,
					TimeInForce: protobuf.TimeInForce_GTC,
					Timestamp:   timestamppb.Now(),
				},
			},
		}

		p.pendingMu.Lock()
		p.pending[orderID] = time.Now()
		p.symbolByOrder[orderID] = sym
		p.pendingMu.Unlock()

		if err := sendFramed(p.conn, msg); err == nil {
			atomic.AddInt64(&p.stats.ordersPlaced, 1)
			atomic.AddInt64(&p.stats.limitOrders, 1)
			p.activeOrders = append(p.activeOrders, orderID)
			if *verbose { log.Printf("[%s] MM quote %s %s %s @%d qty=%d", p.traderID, sym.symbol, side, orderID, price, qty) }
		}
	}
}

func (p *Participant) sendInstitutionalOrder(sym *SymbolState, ph Phase) {
	mid := sym.nextPrice(p.rng)
	qty := int32(100 + p.rng.Intn(900)) // 100–1000 shares

	// Choose order type based on phase
	switch {
	case ph == PhaseOpenAuction && p.rng.Float64() < 0.7:
		// AT_OPEN limit order for auction participation
		price := mid + int64(p.rng.Intn(int(sym.volatility*2))) - sym.volatility
		orderID := p.nextOrderID()
		side := protobuf.OrderSide_BUY
		if p.rng.Float64() < 0.5 { side = protobuf.OrderSide_SELL }

		msg := &protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_NewOrder{
				NewOrder: &protobuf.NewOrderSingle{
					Header:      &protobuf.FXPHeader{ProtocolVersion: "1.1", SequenceNumber: uint64(p.orderSeq)},
					OrderId:     orderID, Sender: p.traderID, Receiver: "FXPExchange",
					Symbol: sym.symbol, Price: price, Side: side, Quantity: qty,
					OrderType:   protobuf.OrderType_LIMIT,
					TimeInForce: protobuf.TimeInForce_AT_OPEN,
					Timestamp:   timestamppb.Now(),
				},
			},
		}
		p.recordAndSendWithSymbol(orderID, msg, sym)
		atomic.AddInt64(&p.stats.limitOrders, 1)
		atomic.AddInt64(&p.stats.atOpenOrders, 1)
		if *verbose { log.Printf("[%s] Institutional AT_OPEN %s qty=%d", p.traderID, sym.symbol, qty) }

	case ph == PhaseCloseAuction && p.rng.Float64() < 0.75:
		// AT_CLOSE (MOC) order
		orderID := p.nextOrderID()
		side := protobuf.OrderSide_BUY
		if p.rng.Float64() < 0.5 { side = protobuf.OrderSide_SELL }

		msg := &protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_NewOrder{
				NewOrder: &protobuf.NewOrderSingle{
					Header:      &protobuf.FXPHeader{ProtocolVersion: "1.1", SequenceNumber: uint64(p.orderSeq)},
					OrderId:     orderID, Sender: p.traderID, Receiver: "FXPExchange",
					Symbol: sym.symbol, Price: mid, Side: side, Quantity: qty,
					OrderType:   protobuf.OrderType_LIMIT,
					TimeInForce: protobuf.TimeInForce_AT_CLOSE,
					Timestamp:   timestamppb.Now(),
				},
			},
		}
		p.recordAndSendWithSymbol(orderID, msg, sym)
		atomic.AddInt64(&p.stats.limitOrders, 1)
		atomic.AddInt64(&p.stats.atCloseOrders, 1)
		if *verbose { log.Printf("[%s] Institutional AT_CLOSE %s qty=%d", p.traderID, sym.symbol, qty) }

	default:
		// Standard limit order — post slightly inside best bid/ask
		offset := int64(1 + p.rng.Intn(int(sym.volatility)))
		side := protobuf.OrderSide_BUY
		price := mid - offset
		if p.rng.Float64() < 0.5 {
			side = protobuf.OrderSide_SELL
			price = mid + offset
		}
		if price < 1 { price = 1 }

		orderID := p.nextOrderID()
		msg := &protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_NewOrder{
				NewOrder: &protobuf.NewOrderSingle{
					Header:      &protobuf.FXPHeader{ProtocolVersion: "1.1", SequenceNumber: uint64(p.orderSeq)},
					OrderId:     orderID, Sender: p.traderID, Receiver: "FXPExchange",
					Symbol: sym.symbol, Price: price, Side: side, Quantity: qty,
					OrderType:   protobuf.OrderType_LIMIT,
					TimeInForce: protobuf.TimeInForce_DAY,
					Timestamp:   timestamppb.Now(),
				},
			},
		}
		p.recordAndSendWithSymbol(orderID, msg, sym)
		atomic.AddInt64(&p.stats.limitOrders, 1)
		p.activeOrders = append(p.activeOrders, orderID)

		// 30% chance of cancel/replace after placing
		if p.rng.Float64() < 0.3 {
			time.AfterFunc(time.Duration(100+p.rng.Intn(500))*time.Millisecond, func() {
				p.sendCancelReplace(orderID, sym, price, qty)
			})
		}
	}
}

func (p *Participant) sendRetailOrder(sym *SymbolState, ph Phase) {
	mid := sym.nextPrice(p.rng)
	qty := int32(1 + p.rng.Intn(20)) // 1–20 shares

	r := p.rng.Float64()
	side := protobuf.OrderSide_BUY
	if p.rng.Float64() < 0.5 { side = protobuf.OrderSide_SELL }

	orderID := p.nextOrderID()

	switch {
	case r < 0.5:
		// Market order — retail traders often just hit market
		msg := &protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_NewOrder{
				NewOrder: &protobuf.NewOrderSingle{
					Header:      &protobuf.FXPHeader{ProtocolVersion: "1.1", SequenceNumber: uint64(p.orderSeq)},
					OrderId:     orderID, Sender: p.traderID, Receiver: "FXPExchange",
					Symbol: sym.symbol, Price: 0, Side: side, Quantity: qty,
					OrderType:   protobuf.OrderType_MARKET,
					TimeInForce: protobuf.TimeInForce_DAY,
					Timestamp:   timestamppb.Now(),
				},
			},
		}
		p.recordAndSendWithSymbol(orderID, msg, sym)
		atomic.AddInt64(&p.stats.marketOrders, 1)
		if *verbose { log.Printf("[%s] Retail MARKET %s %s qty=%d", p.traderID, sym.symbol, side, qty) }

	case r < 0.8:
		// Limit order near mid
		offset := int64(p.rng.Intn(int(sym.volatility * 3)))
		price := mid - offset
		if side == protobuf.OrderSide_SELL { price = mid + offset }
		if price < 1 { price = 1 }

		msg := &protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_NewOrder{
				NewOrder: &protobuf.NewOrderSingle{
					Header:      &protobuf.FXPHeader{ProtocolVersion: "1.1", SequenceNumber: uint64(p.orderSeq)},
					OrderId:     orderID, Sender: p.traderID, Receiver: "FXPExchange",
					Symbol: sym.symbol, Price: price, Side: side, Quantity: qty,
					OrderType:   protobuf.OrderType_LIMIT,
					TimeInForce: protobuf.TimeInForce_DAY,
					Timestamp:   timestamppb.Now(),
				},
			},
		}
		p.recordAndSendWithSymbol(orderID, msg, sym)
		atomic.AddInt64(&p.stats.limitOrders, 1)

	default:
		// IOC — retail impatience
		msg := &protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_NewOrder{
				NewOrder: &protobuf.NewOrderSingle{
					Header:      &protobuf.FXPHeader{ProtocolVersion: "1.1", SequenceNumber: uint64(p.orderSeq)},
					OrderId:     orderID, Sender: p.traderID, Receiver: "FXPExchange",
					Symbol: sym.symbol, Price: mid, Side: side, Quantity: qty,
					OrderType:   protobuf.OrderType_LIMIT,
					TimeInForce: protobuf.TimeInForce_IOC,
					Timestamp:   timestamppb.Now(),
				},
			},
		}
		p.recordAndSendWithSymbol(orderID, msg, sym)
		atomic.AddInt64(&p.stats.iocOrders, 1)
	}
}

func (p *Participant) sendHedgeFundOrder(sym *SymbolState, ph Phase) {
	mid := sym.nextPrice(p.rng)
	qty := int32(50 + p.rng.Intn(450)) // 50–500

	r := p.rng.Float64()
	side := protobuf.OrderSide_BUY
	if p.rng.Float64() < 0.5 { side = protobuf.OrderSide_SELL }

	orderID := p.nextOrderID()

	switch {
	case r < 0.3:
		// Stop order for risk management
		stopOffset := sym.volatility * int64(2+p.rng.Intn(5)) // 2–6 vol units away
		var stopPrice int64
		if side == protobuf.OrderSide_SELL {
			stopPrice = mid - stopOffset // sell stop: trigger if price falls
		} else {
			stopPrice = mid + stopOffset // buy stop: trigger if price rises (breakout)
		}
		if stopPrice < 1 { stopPrice = 1 }

		msg := &protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_NewOrder{
				NewOrder: &protobuf.NewOrderSingle{
					Header:      &protobuf.FXPHeader{ProtocolVersion: "1.1", SequenceNumber: uint64(p.orderSeq)},
					OrderId:     orderID, Sender: p.traderID, Receiver: "FXPExchange",
					Symbol: sym.symbol, Price: 0, Side: side, Quantity: qty,
					OrderType:   protobuf.OrderType_STOP,
					TimeInForce: protobuf.TimeInForce_DAY,
					StopPrice:   stopPrice,
					Timestamp:   timestamppb.Now(),
				},
			},
		}
		p.pendingMu.Lock()
		p.stopOrderIDs[orderID] = true
		p.pendingMu.Unlock()
		p.recordAndSendWithSymbol(orderID, msg, sym)
		atomic.AddInt64(&p.stats.stopOrders, 1)
		if *verbose { log.Printf("[%s] HF STOP %s %s stop@%d qty=%d", p.traderID, sym.symbol, side, stopPrice, qty) }

	case r < 0.6:
		// Aggressive limit order crossing the spread
		price := mid
		msg := &protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_NewOrder{
				NewOrder: &protobuf.NewOrderSingle{
					Header:      &protobuf.FXPHeader{ProtocolVersion: "1.1", SequenceNumber: uint64(p.orderSeq)},
					OrderId:     orderID, Sender: p.traderID, Receiver: "FXPExchange",
					Symbol: sym.symbol, Price: price, Side: side, Quantity: qty,
					OrderType:   protobuf.OrderType_LIMIT,
					TimeInForce: protobuf.TimeInForce_IOC,
					Timestamp:   timestamppb.Now(),
				},
			},
		}
		p.recordAndSendWithSymbol(orderID, msg, sym)
		atomic.AddInt64(&p.stats.iocOrders, 1)

	default:
		// FOK — all or nothing block trade
		msg := &protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_NewOrder{
				NewOrder: &protobuf.NewOrderSingle{
					Header:      &protobuf.FXPHeader{ProtocolVersion: "1.1", SequenceNumber: uint64(p.orderSeq)},
					OrderId:     orderID, Sender: p.traderID, Receiver: "FXPExchange",
					Symbol: sym.symbol, Price: mid, Side: side, Quantity: qty,
					OrderType:   protobuf.OrderType_LIMIT,
					TimeInForce: protobuf.TimeInForce_FOK,
					Timestamp:   timestamppb.Now(),
				},
			},
		}
		p.recordAndSend(orderID, msg)
	}
}

func (p *Participant) recordAndSend(orderID string, msg *protobuf.FXPMessage) {
	p.recordAndSendWithSymbol(orderID, msg, nil)
}

func (p *Participant) recordAndSendWithSymbol(orderID string, msg *protobuf.FXPMessage, sym *SymbolState) {
	p.pendingMu.Lock()
	p.pending[orderID] = time.Now()
	if sym != nil {
		p.symbolByOrder[orderID] = sym
	}
	p.pendingMu.Unlock()

	if err := sendFramed(p.conn, msg); err == nil {
		atomic.AddInt64(&p.stats.ordersPlaced, 1)
	}
}

func (p *Participant) maybeCancelOldOrder() {
	p.pendingMu.Lock()
	if len(p.activeOrders) == 0 {
		p.pendingMu.Unlock()
		return
	}
	// Cancel the oldest resting order
	idx := p.rng.Intn(len(p.activeOrders))
	orderID := p.activeOrders[idx]
	p.activeOrders = append(p.activeOrders[:idx], p.activeOrders[idx+1:]...)
	p.pendingMu.Unlock()

	cancelID := p.nextOrderID()
	msg := &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_OrderCancel{
			OrderCancel: &protobuf.OrderCancelRequest{
				Header:          &protobuf.FXPHeader{ProtocolVersion: "1.1"},
				OrderId:         orderID,
				ClientCancelId:  cancelID,
				Sender:          p.traderID,
				Receiver:        "FXPExchange",
				Timestamp:       timestamppb.Now(),
			},
		},
	}
	if err := sendFramed(p.conn, msg); err == nil {
		atomic.AddInt64(&p.stats.ordersCancelled, 1)
		if *verbose { log.Printf("[%s] CANCEL %s", p.traderID, orderID) }
	}
}

func (p *Participant) sendCancelReplace(orderID string, sym *SymbolState, oldPrice int64, oldQty int32) {
	newPrice := oldPrice + int64(p.rng.Intn(int(sym.volatility*2)))-sym.volatility
	if newPrice < 1 { newPrice = 1 }
	newOrderID := p.nextOrderID()
	replaceID  := p.nextOrderID()

	msg := &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_OrderCancelReplace{
			OrderCancelReplace: &protobuf.OrderCancelReplaceRequest{
				Header:           &protobuf.FXPHeader{ProtocolVersion: "1.1"},
				OrderId:          orderID,
				NewOrderId:       newOrderID,
				ClientReplaceId:  replaceID,
				Sender:           p.traderID,
				Receiver:         "FXPExchange",
				Symbol:           sym.symbol,
				NewPrice:         newPrice,
				NewQuantity:      oldQty,
				Timestamp:        timestamppb.Now(),
			},
		},
	}
	if err := sendFramed(p.conn, msg); err == nil {
		atomic.AddInt64(&p.stats.cancelReplaces, 1)
		if *verbose { log.Printf("[%s] CANCEL/REPLACE %s → %s @%d", p.traderID, orderID, newOrderID, newPrice) }
	}
}

// readLoop processes all incoming messages (ExecutionReports, etc.)
func (p *Participant) readLoop() {
	for {
		p.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		msg, err := readFramed(p.conn)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue // normal timeout, keep reading
			}
			return // connection closed
		}
		p.conn.SetReadDeadline(time.Time{})

		switch payload := msg.Payload.(type) {
		case *protobuf.FXPMessage_ExecutionReport:
			rep := payload.ExecutionReport
			orderID := rep.OrderId
			execType := protobuf.ExecutionType(rep.ExecutionType)

			p.pendingMu.Lock()
			sent, hasPending := p.pending[orderID]
			sym := p.symbolByOrder[orderID]

			// Only record latency and count fills for terminal/fill events
			// and only when this orderID is in our pending map (our own orders).
			// This prevents double-counting counterparty fill reports.
			terminal := execType == protobuf.ExecutionType_EXEC_FILLED ||
				execType == protobuf.ExecutionType_EXEC_CANCELED ||
				execType == protobuf.ExecutionType_EXEC_REJECTED ||
				execType == protobuf.ExecutionType_EXEC_REPLACED ||
				execType == protobuf.ExecutionType_EXEC_EXPIRED ||
				execType == protobuf.ExecutionType_EXEC_NEW // ack latency recorded; stop triggers get their own fill report

			// Check if this is a stop trigger: EXEC_NEW for an order no longer in pending
			// means the stop was already ack'd (first EXEC_NEW cleared pending) and now fired.
			isStopOrder := p.stopOrderIDs[orderID]
			if !hasPending && execType == protobuf.ExecutionType_EXEC_NEW && isStopOrder {
				atomic.AddInt64(&p.stats.stopsFired, 1)
				if *verbose { log.Printf("[%s] STOP TRIGGERED %s", p.traderID, orderID) }
			}

			if hasPending {
				if execType == protobuf.ExecutionType_EXEC_FILLED || execType == protobuf.ExecutionType_EXEC_NEW {
					latency := time.Since(sent)
					p.stats.recordLatency(latency)
				}
				if terminal {
					delete(p.pending, orderID)
					delete(p.symbolByOrder, orderID)
					// Keep stopOrderIDs until the order is truly done (not just ack'd)
					// EXEC_NEW for a stop just means it's parked — it may still trigger.
					// Only remove on fill/cancel/reject/replace/expire.
					if execType != protobuf.ExecutionType_EXEC_NEW {
						delete(p.stopOrderIDs, orderID)
					}
					// Remove from activeOrders
					for i, id := range p.activeOrders {
						if id == orderID {
							p.activeOrders = append(p.activeOrders[:i], p.activeOrders[i+1:]...)
							break
						}
					}
				}
			}
			p.pendingMu.Unlock()

			switch execType {
			case protobuf.ExecutionType_EXEC_FILLED:
				if hasPending { // only count our own fills once
					atomic.AddInt64(&p.stats.ordersFilled, 1)
					if sym != nil {
						sym.recordFill(rep.ExecutionPrice, int64(rep.ExecutedQuantity))
					}
					if *verbose { log.Printf("[%s] FILLED %s @%d qty=%d", p.traderID, orderID, rep.ExecutionPrice, rep.ExecutedQuantity) }
				}
			case protobuf.ExecutionType_EXEC_CANCELED:
				// cancel confirmed — already cleaned from pending above
			case protobuf.ExecutionType_EXEC_REJECTED:
				// Only count as a rejection if this was a NewOrder (not a cancel of an already-filled order).
				// Cancel requests for filled orders return EXEC_REJECTED normally — not an error.
				if hasPending {
					// Check if this was a cancel request by seeing if we have a symbol mapping.
					// NewOrders always have a symbol; cancel requests use the original orderID
					// which may no longer have a symbol entry after the fill cleaned it up.
					sym2 := p.symbolByOrder[orderID]
					if sym2 != nil {
						atomic.AddInt64(&p.stats.ordersRejected, 1)
					}
					// else: rejection of an already-filled order cancel — expected, don't count
				}
			}
		}
	}
}

// ── Phase scheduler ───────────────────────────────────────────────────────────

func runPhaseScheduler(phase *Phase, phaseMu *sync.RWMutex, totalDuration time.Duration, done chan struct{}) {
	// Scale session phases to wall-clock duration
	// Real day: pre=9hr, open_auction=1min, continuous=5h25min, close_auction=5min
	// We compress into totalDuration proportionally
	total := float64(totalDuration)
	phases := []struct {
		ph       Phase
		fraction float64
		name     string
	}{
		{PhasePreMarket,    0.05, "PRE_MARKET"},
		{PhaseOpenAuction,  0.05, "OPEN_AUCTION"},
		{PhaseContinuous,   0.80, "CONTINUOUS"},
		{PhaseCloseAuction, 0.08, "CLOSE_AUCTION"},
		{PhaseClosed,       0.02, "CLOSED"},
	}

	for _, p := range phases {
		phaseMu.Lock()
		*phase = p.ph
		phaseMu.Unlock()
		fmt.Printf("\n📅 Phase: %s\n", p.name)

		select {
		case <-done:
			return
		case <-time.After(time.Duration(total * p.fraction)):
		}
	}
	close(done)
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	flag.Parse()

	secret := os.Getenv("FXP_JWT_SECRET")
	if secret == "" {
		log.Fatal("FXP_JWT_SECRET not set")
	}

	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(*seed))

	fmt.Printf("\n╔══════════════════════════════════════════════════════╗\n")
	fmt.Printf("║         FXP Trading Day Simulation                   ║\n")
	fmt.Printf("╠══════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Symbols:       %-36d║\n", *numSymbols)
	fmt.Printf("║  Participants:  %-36d║\n", *numParticipants)
	fmt.Printf("║  Duration:      %-36s║\n", fmt.Sprintf("%ds", *durationSec))
	fmt.Printf("║  Seed:          %-36d║\n", *seed)
	fmt.Printf("╚══════════════════════════════════════════════════════╝\n")

	// Verify gateway is reachable
	testConn, err := net.DialTimeout("tcp", gatewayAddr, 2*time.Second)
	if err != nil {
		log.Fatalf("Gateway not reachable at %s: %v\nMake sure both servers are running.", gatewayAddr, err)
	}
	testConn.Close()
	fmt.Println("Gateway reachable ✓")

	// Build symbol universe
	baseSymbols := []struct{ name string; price int64; vol int64 }{
		{"AAPL",    18500, 15},
		{"MSFT",    37500, 20},
		{"GOOGL",   17200, 18},
		{"AMZN",    19800, 22},
		{"NVDA",    87500, 50},
		{"TSLA",    25000, 35},
		{"META",    50000, 25},
		{"JPM",     20000, 12},
		{"ETH/USD", 320000, 200},
		{"BTC/USD", 6800000, 500},
	}
	n := *numSymbols
	if n > len(baseSymbols) { n = len(baseSymbols) }

	symbols := make([]*SymbolState, n)
	for i := 0; i < n; i++ {
		s := baseSymbols[i]
		symbols[i] = newSymbolState(s.name, s.price, s.vol)
		fmt.Printf("  📈 %s  mid=$%.2f  vol=$%.2f\n", s.name, float64(s.price)/100, float64(s.vol)/100)
	}

	// Shared state
	stats  := &SimStats{}
	phase  := PhasePreMarket
	phaseMu := &sync.RWMutex{}
	done   := make(chan struct{})

	// Assign participant types
	typeDistribution := []ParticipantType{
		TypeMarketMaker,   TypeMarketMaker,   // 2 market makers
		TypeInstitutional, TypeInstitutional, TypeInstitutional, // 3 institutional
		TypeHedgeFund,     TypeHedgeFund,     // 2 hedge funds
		TypeRetail,        TypeRetail,        TypeRetail, TypeRetail, // 4+ retail
	}

	// Build and connect participants
	participants := make([]*Participant, *numParticipants)
	fmt.Printf("\nConnecting %d participants...\n", *numParticipants)

	for i := 0; i < *numParticipants; i++ {
		ptype := typeDistribution[i%len(typeDistribution)]
		traderID := fmt.Sprintf("%s-%03d", ptype, i)
		sessionID := fmt.Sprintf("sim-%d-%d", *seed, i)

		conn, err := connectAndLogon(traderID, sessionID)
		if err != nil {
			log.Fatalf("Participant %d connect failed: %v", i, err)
		}

		// Assign 1–3 symbols to each participant
		numSymsForParticipant := 1 + rng.Intn(min(3, n))
		symList := make([]*SymbolState, numSymsForParticipant)
		for j := 0; j < numSymsForParticipant; j++ {
			symList[j] = symbols[(i+j)%n]
		}

		participants[i] = &Participant{
			id:      i,
			ptype:   ptype,
			traderID: traderID,
			conn:    conn,
			symbols: symList,
			rng:     rand.New(rand.NewSource(*seed + int64(i)*31337)),
			stats:   stats,
			phase:   &phase,
			phaseMu: phaseMu,
			pending:       make(map[string]time.Time),
			symbolByOrder: make(map[string]*SymbolState),
			stopOrderIDs:  make(map[string]bool),
		}
	}
	fmt.Printf("✅ All participants connected\n")

	// Start participants
	var wg sync.WaitGroup
	for _, p := range participants {
		wg.Add(1)
		go p.run(&wg, done)
	}

	// Run phase scheduler
	go runPhaseScheduler(&phase, phaseMu, time.Duration(*durationSec)*time.Second, done)

	// Progress ticker
	start := time.Now()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			goto finished
		case <-ticker.C:
			elapsed := time.Since(start).Round(time.Second)
			phaseMu.RLock()
			ph := phase
			phaseMu.RUnlock()
			fmt.Printf("  [%s] Phase=%-15s Orders=%d Fills=%d Cancels=%d\n",
				elapsed, ph,
				atomic.LoadInt64(&stats.ordersPlaced),
				atomic.LoadInt64(&stats.ordersFilled),
				atomic.LoadInt64(&stats.ordersCancelled),
			)
		}
	}

finished:
	wg.Wait()
	elapsed := time.Since(start)

	// Close connections
	for _, p := range participants {
		p.conn.Close()
	}

	// Print results
	fmt.Printf("\n╔══════════════════════════════════════════════════════╗\n")
	fmt.Printf("║         FXP Trading Day Simulation — Results         ║\n")
	fmt.Printf("╠══════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Duration:          %-32s║\n", elapsed.Round(time.Millisecond))
	fmt.Printf("╠══════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Orders placed:     %-32d║\n", stats.ordersPlaced)
	fmt.Printf("║  Orders filled:     %-32d║\n", stats.ordersFilled)
	fmt.Printf("║  Orders cancelled:  %-32d║\n", stats.ordersCancelled)
	fmt.Printf("║  Orders rejected:   %-32d║\n", stats.ordersRejected)
	fmt.Printf("║  Stops fired:       %-32d║\n", stats.stopsFired)
	fmt.Printf("║  Cancel/Replaces:   %-32d║\n", stats.cancelReplaces)
	fmt.Printf("╠══════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  By order type:                                      ║\n")
	fmt.Printf("║  Limit:             %-32d║\n", stats.limitOrders)
	fmt.Printf("║  Market:            %-32d║\n", stats.marketOrders)
	fmt.Printf("║  Stop:              %-32d║\n", stats.stopOrders)
	fmt.Printf("║  IOC:               %-32d║\n", stats.iocOrders)
	fmt.Printf("║  AT_OPEN:           %-32d║\n", stats.atOpenOrders)
	fmt.Printf("║  AT_CLOSE:          %-32d║\n", stats.atCloseOrders)
	fmt.Printf("╠══════════════════════════════════════════════════════╣\n")
	total := stats.ordersPlaced
	fills := stats.ordersFilled
	if total > 0 {
		fillRate := float64(fills) / float64(total) * 100
		cancelRate := float64(stats.ordersCancelled) / float64(total) * 100
		fmt.Printf("║  Fill rate:         %-31s║\n", fmt.Sprintf("%.1f%%", fillRate))
		fmt.Printf("║  Cancel rate:       %-31s║\n", fmt.Sprintf("%.1f%%", cancelRate))
	}
	fmt.Printf("╠══════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Round-trip latency:                                 ║\n")
	fmt.Printf("║  p50:               %-32s║\n", stats.percentile(50))
	fmt.Printf("║  p95:               %-32s║\n", stats.percentile(95))
	fmt.Printf("║  p99:               %-32s║\n", stats.percentile(99))
	fmt.Printf("╠══════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Symbol summary:                                     ║\n")
	for _, sym := range symbols {
		fmt.Printf("║  %-8s open=$%-8.2f last=$%-8.2f vol=%-7d║\n",
			sym.symbol,
			float64(sym.openPrice)/100,
			float64(sym.lastPrice)/100,
			sym.volume,
		)
	}
	fmt.Printf("╚══════════════════════════════════════════════════════╝\n")
}

func min(a, b int) int {
	if a < b { return a }
	return b
}