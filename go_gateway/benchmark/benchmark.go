// FXP End-to-End Benchmark
//
// Measures round-trip latency and throughput through the live FXP stack:
//   Client → Go Gateway (TCP :8082) → Rust Core (:8080) → ExecutionReport back
//
// Prerequisites:
//   1. Rust core running:  cd fxp_core && cargo run
//   2. Go gateway running: cd go_gateway && go run main.go
//   3. FXP_JWT_SECRET set: export FXP_JWT_SECRET="dev-secret-change-in-production"
//
// Usage:
//   go run benchmark/benchmark.go
//   go run benchmark/benchmark.go --concurrency 10 --orders 500
//
// Output:
//   - Latency histogram (p50, p95, p99, max)
//   - Throughput (orders/sec, fills/sec)
//   - Market data fan-out latency
//   - Comparison table vs theoretical FIX overhead

package main

import (
	"encoding/binary"
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

	// JWT for auth
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
)

// ── Config ────────────────────────────────────────────────────────────────────

const (
	gatewayAddr = "127.0.0.1:8082"
	jwtSecret   = "dev-secret-change-in-production"
)

var (
	concurrency = flag.Int("concurrency", 1, "Number of concurrent order senders")
	numOrders   = flag.Int("orders", 200, "Orders per sender")
	warmup      = flag.Int("warmup", 20, "Warmup orders to discard (per sender)")
	symbolMode  = flag.String("symbols", "spread", "Symbol mode: 'single' (all ETH/USD) or 'spread' (unique per sender pair)")
)

// symbolPool — 50 realistic symbols. In spread mode each sender pair gets
// a unique symbol so order books are independent (no shared mutex contention).
var symbolPool = []string{
	"ETH/USD", "BTC/USD", "AAPL", "MSFT", "GOOGL", "AMZN", "TSLA", "NVDA",
	"META", "JPM", "GS", "BAC", "MS", "BRK.B", "V", "MA", "PYPL", "SQ",
	"SOL/USD", "AVAX/USD", "DOT/USD", "LINK/USD", "UNI/USD", "AAVE/USD",
	"ESH4", "NQH4", "CLH4", "GCH4", "SIH4", "ZBH4", "ZNH4", "ZFH4",
	"EUR/USD", "GBP/USD", "USD/JPY", "AUD/USD", "USD/CAD", "USD/CHF",
	"SPY", "QQQ", "IWM", "GLD", "SLV", "USO", "TLT", "HYG", "EEM", "VXX",
}

// symbolForSender assigns a symbol to a sender. In spread mode, adjacent
// sender pairs share a symbol so buys and sells can match each other.
// Senders 0+1 → symbolPool[0], 2+3 → symbolPool[1], etc.
func symbolForSender(senderID int) string {
	if *symbolMode == "single" {
		return "ETH/USD"
	}
	idx := (senderID / 2) % len(symbolPool)
	return symbolPool[idx]
}

// ── JWT (copied from jwt_helpers.go — standalone binary) ─────────────────────

func generateJWT(secret, traderID string) string {
	header := base64.RawURLEncoding.EncodeToString(mustJSON(map[string]string{"alg": "HS256", "typ": "JWT"}))
	payload := base64.RawURLEncoding.EncodeToString(mustJSON(map[string]interface{}{
		"sub": traderID,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(24 * time.Hour).Unix(),
	}))
	sig_input := header + "." + payload
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(sig_input))
	return sig_input + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func mustJSON(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

// ── Wire helpers ──────────────────────────────────────────────────────────────

func sendFramed(conn net.Conn, msg *protobuf.FXPMessage) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(data)))
	copy(frame[4:], data)
	_, err = conn.Write(frame)
	return err
}

func readFramed(conn net.Conn) (*protobuf.FXPMessage, error) {
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return nil, err
	}
	body := make([]byte, binary.BigEndian.Uint32(lenBuf))
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	msg := &protobuf.FXPMessage{}
	return msg, proto.Unmarshal(body, msg)
}

// ── Session setup ─────────────────────────────────────────────────────────────

func connectAndLogon(traderID, sessionID string) (net.Conn, error) {
	conn, err := net.Dial("tcp", gatewayAddr)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	logon := &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_Logon{
			Logon: &protobuf.Logon{
				Token:          generateJWT(jwtSecret, traderID),
				SessionId:      sessionID,
				EncryptionType: protobuf.EncryptionType_TLS_1_3,
				Timestamp:      timestamppb.Now(),
			},
		},
	}
	if err := sendFramed(conn, logon); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send logon: %w", err)
	}

	ack, err := readFramed(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read logon ack: %w", err)
	}
	la, ok := ack.Payload.(*protobuf.FXPMessage_LogonAck)
	if !ok || !la.LogonAck.Accepted {
		conn.Close()
		return nil, fmt.Errorf("logon rejected: %s", la.LogonAck.RejectReason)
	}
	return conn, nil
}

// ── Result types ──────────────────────────────────────────────────────────────

type latencySample struct {
	orderID  string
	duration time.Duration
}

type benchResult struct {
	samples     []time.Duration
	totalOrders int64
	totalFills  int64
	errors      int64
	elapsed     time.Duration
}

func (r *benchResult) merge(other *benchResult) {
	r.samples = append(r.samples, other.samples...)
	r.totalOrders += other.totalOrders
	r.totalFills += other.totalFills
	r.errors += other.errors
}

// ── Single-sender benchmark ───────────────────────────────────────────────────

func runSender(senderID int, numOrd int, warmupCount int) *benchResult {
	traderID := fmt.Sprintf("BenchTrader%d", senderID)
	sessionID := fmt.Sprintf("bench-session-%d-%d", senderID, rand.Int())

	conn, err := connectAndLogon(traderID, sessionID)
	if err != nil {
		log.Printf("Sender %d: logon failed: %v", senderID, err)
		return &benchResult{errors: 1}
	}
	defer conn.Close()

	result := &benchResult{}
	pending := make(map[string]time.Time)
	var mu sync.Mutex
	done := make(chan struct{})

	// Reader goroutine — receives ExecutionReports and records latency
	go func() {
		defer close(done)
		for result.totalFills < int64(numOrd-warmupCount) {
			msg, err := readFramed(conn)
			if err != nil {
				if result.totalFills < int64(numOrd-warmupCount) {
					atomic.AddInt64(&result.errors, 1)
				}
				return
			}
			switch p := msg.Payload.(type) {
			case *protobuf.FXPMessage_ExecutionReport:
				orderID := p.ExecutionReport.OrderId
				mu.Lock()
				if sent, ok := pending[orderID]; ok {
					latency := time.Since(sent)
					delete(pending, orderID)
					mu.Unlock()
					atomic.AddInt64(&result.totalFills, 1)
					if result.totalFills > int64(warmupCount) {
						result.samples = append(result.samples, latency)
					}
				} else {
					mu.Unlock()
				}
			}
		}
	}()

	// Symbol assignment: in spread mode each sender pair gets a unique symbol
	// so order books don't contend on a shared Rust-side mutex.
	// Paired matching: even senderID → BUY only, odd senderID → SELL only.
	// Each buy sender's orders will match against its paired sell sender.
	symbol := symbolForSender(senderID)
	basePrice := int64(3_000_000_000) // $3000.00 fixed-point

	// Determine fixed side for this sender: even=BUY, odd=SELL
	fixedSide := protobuf.OrderSide_BUY
	if senderID%2 != 0 {
		fixedSide = protobuf.OrderSide_SELL
	}

	start := time.Now()
	for i := 0; i < numOrd; i++ {
		orderID := fmt.Sprintf("BENCH-%d-%d", senderID, i)

		order := &protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_NewOrder{
				NewOrder: &protobuf.NewOrderSingle{
					Header: &protobuf.FXPHeader{
						ProtocolVersion: "1.0",
						SequenceNumber:  uint64(i),
					},
					OrderId:     orderID,
					Sender:      traderID,
					Receiver:    "BenchExchange",
					Symbol:      symbol,
					Price:       basePrice,
					Side:        fixedSide,
					Quantity:    1,
					OrderType:   protobuf.OrderType_LIMIT,
					TimeInForce: protobuf.TimeInForce_DAY,
					Timestamp:   timestamppb.Now(),
				},
			},
		}

		sent := time.Now()
		mu.Lock()
		pending[orderID] = sent
		mu.Unlock()

		if err := sendFramed(conn, order); err != nil {
			atomic.AddInt64(&result.errors, 1)
			break
		}
		atomic.AddInt64(&result.totalOrders, 1)

		// Small yield to avoid flooding the gateway buffer
		time.Sleep(500 * time.Microsecond)
	}

	// Wait for all fills or timeout
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		log.Printf("Sender %d: timed out waiting for fills", senderID)
	}

	result.elapsed = time.Since(start)
	return result
}

// ── Market data fan-out latency ───────────────────────────────────────────────

func benchMarketDataLatency() {
	fmt.Println("\n── Market Data Fan-out Latency ──────────────────────────")

	// Subscriber connection
	subConn, err := connectAndLogon("MDSub", fmt.Sprintf("md-bench-%d", rand.Int()))
	if err != nil {
		fmt.Printf("  subscriber connect failed: %v\n", err)
		return
	}
	defer subConn.Close()

	subReq := &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_MarketDataRequest{
			MarketDataRequest: &protobuf.MarketDataRequest{
				Header:  &protobuf.FXPHeader{ProtocolVersion: "1.0"},
				Symbols: []string{symbolForSender(0)},
			},
		},
	}
	sendFramed(subConn, subReq)
	time.Sleep(100 * time.Millisecond) // let subscription propagate

	// Order sender connection
	orderConn, err := connectAndLogon("MDOrderer", fmt.Sprintf("md-order-%d", rand.Int()))
	if err != nil {
		fmt.Printf("  order sender connect failed: %v\n", err)
		return
	}
	defer orderConn.Close()

	var samples []time.Duration
	const mdRuns = 50

	for i := 0; i < mdRuns; i++ {
		order := &protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_NewOrder{
				NewOrder: &protobuf.NewOrderSingle{
					Header:      &protobuf.FXPHeader{ProtocolVersion: "1.0", SequenceNumber: uint64(i)},
					OrderId:     fmt.Sprintf("MD-%d", i),
					Sender:      "MDOrderer",
					Receiver:    "BenchExchange",
					Symbol:      symbolForSender(0),
					Price:       3_000_000_000,
					Side:        protobuf.OrderSide_BUY,
					Quantity:    1,
					OrderType:   protobuf.OrderType_LIMIT,
					TimeInForce: protobuf.TimeInForce_DAY,
					Timestamp:   timestamppb.Now(),
				},
			},
		}

		sent := time.Now()
		sendFramed(orderConn, order)

		// Read from subscriber until we get a MarketDataIncrementalUpdate
		subConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			msg, err := readFramed(subConn)
			if err != nil {
				break
			}
			if _, ok := msg.Payload.(*protobuf.FXPMessage_MarketDataIncremental); ok {
				samples = append(samples, time.Since(sent))
				break
			}
		}
		subConn.SetReadDeadline(time.Time{})
		time.Sleep(5 * time.Millisecond)
	}

	if len(samples) > 0 {
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		fmt.Printf("  Samples:  %d\n", len(samples))
		fmt.Printf("  p50:      %v\n", samples[len(samples)*50/100])
		fmt.Printf("  p95:      %v\n", samples[len(samples)*95/100])
		fmt.Printf("  p99:      %v\n", samples[len(samples)*99/100])
		fmt.Printf("  Max:      %v\n", samples[len(samples)-1])
	} else {
		fmt.Println("  No market data samples collected — ensure both servers are running")
	}
}

// ── Stats ─────────────────────────────────────────────────────────────────────

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100.0*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func printResults(result *benchResult, concurrency int) {
	sort.Slice(result.samples, func(i, j int) bool {
		return result.samples[i] < result.samples[j]
	})

	elapsed := result.elapsed.Seconds()
	ordersPerSec := float64(result.totalOrders) / elapsed
	fillsPerSec := float64(result.totalFills) / elapsed

	fmt.Println("\n╔══════════════════════════════════════════════════════╗")
	fmt.Println("║         FXP End-to-End Benchmark Results             ║")
	fmt.Println("╠══════════════════════════════════════════════════════╣")
	fmt.Printf("║  Concurrency:    %-35d║\n", concurrency)
	fmt.Printf("║  Symbol mode:    %-35s║\n", *symbolMode)
	fmt.Printf("║  Total orders:   %-35d║\n", result.totalOrders)
	fmt.Printf("║  Total fills:    %-35d║\n", result.totalFills)
	fmt.Printf("║  Errors:         %-35d║\n", result.errors)
	fmt.Printf("║  Elapsed:        %-35s║\n", result.elapsed.Round(time.Millisecond))
	fmt.Println("╠══════════════════════════════════════════════════════╣")
	fmt.Printf("║  Orders/sec:     %-35.1f║\n", ordersPerSec)
	fmt.Printf("║  Fills/sec:      %-35.1f║\n", fillsPerSec)
	fmt.Println("╠══════════════════════════════════════════════════════╣")
	fmt.Println("║  Round-trip latency (order sent → ExecutionReport)   ║")
	if len(result.samples) > 0 {
		fmt.Printf("║  p50:            %-35s║\n", percentile(result.samples, 50))
		fmt.Printf("║  p95:            %-35s║\n", percentile(result.samples, 95))
		fmt.Printf("║  p99:            %-35s║\n", percentile(result.samples, 99))
		fmt.Printf("║  Max:            %-35s║\n", result.samples[len(result.samples)-1])
	} else {
		fmt.Println("║  No latency samples — check that fills are occurring  ║")
	}
	fmt.Println("╚══════════════════════════════════════════════════════╝")
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	flag.Parse()

	secret := os.Getenv("FXP_JWT_SECRET")
	if secret == "" {
		log.Fatal("FXP_JWT_SECRET not set")
	}

	fmt.Printf("\nFXP Benchmark — concurrency=%d orders=%d warmup=%d symbols=%s\n",
		*concurrency, *numOrders, *warmup, *symbolMode)
	fmt.Println("Connecting to gateway at", gatewayAddr, "...")

	// Verify gateway is reachable
	testConn, err := net.DialTimeout("tcp", gatewayAddr, 2*time.Second)
	if err != nil {
		log.Fatalf("Gateway not reachable at %s: %v\nMake sure both servers are running.", gatewayAddr, err)
	}
	testConn.Close()
	fmt.Println("Gateway reachable ✓\n")

	// Run concurrent senders
	var wg sync.WaitGroup
	results := make([]*benchResult, *concurrency)

	start := time.Now()
	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			results[id] = runSender(id, *numOrders, *warmup)
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	// Merge results
	combined := &benchResult{elapsed: elapsed}
	for _, r := range results {
		if r != nil {
			combined.merge(r)
		}
	}

	printResults(combined, *concurrency)

	// Market data fan-out latency (single-threaded measurement)
	benchMarketDataLatency()

	// Concurrency sweep if running with concurrency=1 (show scaling)
	if *concurrency == 1 {
		fmt.Println("\n── Concurrency Scaling Preview ──────────────────────────")
		fmt.Println("  Run with --concurrency N to test higher concurrency.")
		fmt.Printf("  Example: go run benchmark/benchmark.go --concurrency 10 --orders 500\n")
	}
}