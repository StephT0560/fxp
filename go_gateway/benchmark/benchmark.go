// SPDX-License-Identifier: MIT
// Live paired-order benchmark. Open-loop latency starts at the absolute intended
// send time; closed-loop has one outstanding acknowledgment per connection.
// Both acknowledgment and final-fill endpoints are measured, never conflated.
package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	hdr "github.com/HdrHistogram/hdrhistogram-go"
	"go_gateway/diagnostics"
	"go_gateway/protobuf"
	"go_gateway/symbolconfig"
	"go_gateway/test_clients/devjwt"
	"go_gateway/tlsconfig"
	"google.golang.org/protobuf/proto"
)

const maxFrame = 4 * 1024 * 1024
const histogramMax = int64(24 * time.Hour)

type config struct {
	TLS         tlsconfig.ClientOptions `json:"tls"`
	Subscribers int                     `json:"market_data_subscribers"`
	Pacer       string                  `json:"pacer"`
	Mode        string                  `json:"mode"`
	Address     string                  `json:"gateway"`
	Concurrency int                     `json:"connections"`
	Orders      int                     `json:"measured_orders_per_connection"`
	Warmup      int                     `json:"warmup_orders_per_connection"`
	Repetitions int                     `json:"repetitions"`
	Rate        int                     `json:"open_loop_orders_per_second_per_connection"`
	Timeout     time.Duration           `json:"response_timeout_ns"`
	Symbol      string                  `json:"symbol"`
	Symbols     string                  `json:"symbol_mode"`
	Quantity    int32                   `json:"quantity"`
	Price       int64                   `json:"default_price_minor_units"`
	Pinning     string                  `json:"pinning_notes"`
}

func benchmarkSecret() string {
	if v := os.Getenv("FXP_JWT_SECRET"); v != "" {
		return v
	}
	return "dev-secret-change-in-production"
}
func symbolFor(c config, id int) string {
	if c.Symbols == "spread" {
		return c.Symbol + strconv.Itoa(id/2)
	}
	return c.Symbol
}
func validate(c config) error {
	if c.Pacer != "" && c.Pacer != "sleep" && c.Pacer != "busy-spin" && c.Pacer != "hybrid" {
		return errors.New("pacer must be sleep, busy-spin or hybrid")
	}
	if c.Mode != "open" && c.Mode != "closed" {
		return errors.New("mode must be open or closed")
	}
	if c.Subscribers < 0 || c.Subscribers > 1024 {
		return errors.New("subscribers must be 0..1024")
	}
	if c.Concurrency < 1 || c.Concurrency > 1024 || c.Orders < 2 || c.Orders > 10_000_000 || c.Orders%2 != 0 || c.Warmup < 2 || c.Warmup > 10_000_000 || c.Warmup%2 != 0 || c.Repetitions < 1 || c.Repetitions > 100 || c.Timeout <= 0 || c.Timeout > time.Hour {
		return errors.New("require 1..1024 connections, even orders/warmup in 2..10000000, 1..100 repetitions, timeout in (0,1h]")
	}
	if c.Mode == "open" && (c.Rate <= 0 || c.Rate > 1_000_000_000 || time.Duration(c.Orders)*time.Second/time.Duration(c.Rate) > time.Hour || time.Duration(c.Warmup)*time.Second/time.Duration(c.Rate) > time.Hour) {
		return errors.New("open-loop rate must be 1..1e9/s with each phase <=1h")
	}
	if c.Symbols != "single" && c.Symbols != "spread" {
		return errors.New("symbols must be single or spread")
	}
	if c.Quantity <= 0 || strings.TrimSpace(c.Symbol) == "" {
		return errors.New("positive quantity and nonempty symbol required")
	}
	for id := 0; id < c.Concurrency; id++ {
		symbol := symbolFor(c, id)
		r := symbolconfig.Current().For(symbol)
		price, err := symbolconfig.Current().ParsePrice(symbol, "3000.00")
		if err != nil || price <= 0 || price%r.TickSize != 0 || uint32(c.Quantity) < r.MinQuantity || uint32(c.Quantity) > r.MaxQuantity || uint32(c.Quantity)%r.LotSize != 0 {
			return fmt.Errorf("benchmark $3000.00 price or quantity violates symbol %q config", symbol)
		}
	}
	return nil
}
func sendFramed(conn net.Conn, msg *protobuf.FXPMessage) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	if len(data) > maxFrame {
		return errors.New("oversize outbound frame")
	}
	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(data)))
	copy(frame[4:], data)
	for len(frame) > 0 {
		n, err := conn.Write(frame)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
		frame = frame[n:]
	}
	return nil
}
func readFramed(conn net.Conn) (*protobuf.FXPMessage, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(conn, prefix[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(prefix[:])
	if n == 0 || n > maxFrame {
		return nil, errors.New("invalid frame length")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	m := new(protobuf.FXPMessage)
	return m, proto.Unmarshal(body, m)
}
func connect(c config, id int, nonce string) (net.Conn, error) {
	conn, err := c.TLS.Dial(context.Background(), c.Address, c.Timeout)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()
	if err = conn.SetDeadline(time.Now().Add(c.Timeout)); err != nil {
		return nil, err
	}
	encryption := protobuf.EncryptionType_NONE
	if tlsconfig.Version(conn) == tls.VersionTLS13 {
		encryption = protobuf.EncryptionType_TLS_1_3
	} else if tlsconfig.Version(conn) == tls.VersionTLS12 {
		encryption = protobuf.EncryptionType_TLS_1_2
	}
	m := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_Logon{Logon: &protobuf.Logon{Token: devjwt.Generate(benchmarkSecret(), fmt.Sprintf("bench-%s-%d", nonce, id)), SessionId: fmt.Sprintf("bench-%s-%d", nonce, id), EncryptionType: encryption}}}
	if err = sendFramed(conn, m); err != nil {
		return nil, err
	}
	ack, err := readFramed(conn)
	if err != nil {
		return nil, err
	}
	if ack.GetLogonAck() == nil || !ack.GetLogonAck().Accepted {
		return nil, errors.New("benchmark Logon rejected")
	}
	ok = true
	return conn, nil
}
func histogram() *hdr.Histogram { return hdr.New(1, histogramMax, 3) }

// paceUntil never advances the intended schedule. Hybrid sleeps until 100us
// before the deadline, then spins; all modes keep overdue sends overdue.
func paceUntil(deadline time.Time, mode string) {
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return
		}
		switch mode {
		case "busy-spin":
		case "hybrid":
			if remaining > 100*time.Microsecond {
				time.Sleep(remaining - 100*time.Microsecond)
			}
		default:
			time.Sleep(remaining)
		}
	}
}

type sendStamp struct{ intended, actual time.Time }
type phaseResult struct {
	Ack, Fill, SendLag, AckAfterSend *hdr.Histogram
	MaxSendLag                       int64
}

func runClientPhase(conn net.Conn, c config, symbol, prefix string, start time.Time, count int) (phaseResult, error) {
	result := phaseResult{Ack: histogram(), Fill: histogram(), SendLag: histogram(), AckAfterSend: histogram()}
	duration := time.Duration(0)
	if c.Mode == "open" {
		duration = time.Duration(count) * time.Second / time.Duration(c.Rate)
	}
	if err := conn.SetDeadline(start.Add(duration + c.Timeout)); err != nil {
		return result, err
	}
	price, err := symbolconfig.Current().ParsePrice(symbol, "3000.00")
	if err != nil {
		return result, err
	}
	var pending sync.Map
	acknowledged := make(chan struct{}, 1)
	received := make(chan error, 1)
	go func() {
		var readerError error
		defer func() {
			if readerError != nil {
				_ = conn.Close()
			}
			received <- readerError
		}()
		seenAck, seenFill := map[string]bool{}, map[string]bool{}
		acks, fills := 0, 0
		for acks < count || fills < count {
			m, err := readFramed(conn)
			if err != nil {
				readerError = fmt.Errorf("read with ack=%d/%d fill=%d/%d: %w", acks, count, fills, count, err)
				return
			}
			r := m.GetExecutionReport()
			if r == nil {
				if m.GetErrorMessage() != nil {
					readerError = fmt.Errorf("core error: %s", m.GetErrorMessage().ErrorMessage)
					return
				}
				continue
			}
			value, ok := pending.Load(r.OrderId)
			if !ok {
				readerError = fmt.Errorf("unexpected execution report order %q", r.OrderId)
				return
			}
			stamp := value.(sendStamp)
			now := time.Now()
			diagnostics.Message("client_receive", m)
			latency := now.Sub(stamp.intended).Nanoseconds()
			if latency < 1 {
				latency = 1
			}
			switch r.ExecutionType {
			case protobuf.ExecutionType_EXEC_NEW:
				if seenAck[r.OrderId] {
					readerError = errors.New("duplicate acknowledgment")
					return
				}
				if err := result.AckAfterSend.RecordValue(max(1, now.Sub(stamp.actual).Nanoseconds())); err != nil {
					readerError = err
					return
				}
				if err := result.SendLag.RecordValue(max(1, stamp.actual.Sub(stamp.intended).Nanoseconds())); err != nil {
					readerError = err
					return
				}
				seenAck[r.OrderId] = true
				acks++
				if err := result.Ack.RecordValue(latency); err != nil {
					readerError = err
					return
				}
				if c.Mode == "closed" {
					acknowledged <- struct{}{}
				}
			case protobuf.ExecutionType_EXEC_FILLED:
				if r.OrderStatus != protobuf.OrderStatus_ORDER_FILLED {
					continue
				}
				if seenFill[r.OrderId] {
					readerError = errors.New("duplicate terminal fill")
					return
				}
				seenFill[r.OrderId] = true
				fills++
				if err := result.Fill.RecordValue(latency); err != nil {
					readerError = err
					return
				}
			case protobuf.ExecutionType_EXEC_REJECTED, protobuf.ExecutionType_EXEC_CANCELED, protobuf.ExecutionType_EXEC_EXPIRED:
				readerError = fmt.Errorf("order %s did not fill: %s", r.OrderId, r.RejectReason)
				return
			}
		}
		readerError = nil
	}()
	// Every return joins the reader. Closing the connection on failure unblocks
	// both directions; failed/incomplete runs never produce success percentiles.
	fail := func(err error) (phaseResult, error) {
		_ = conn.Close()
		readerErr := <-received
		return result, errors.Join(err, readerErr)
	}
	if wait := time.Until(start); wait > 0 {
		time.Sleep(wait)
	}
	for i := 0; i < count; i++ {
		intended := time.Now()
		if c.Mode == "open" {
			intended = start.Add(time.Duration(i) * time.Second / time.Duration(c.Rate))
		}
		paceUntil(intended, c.Pacer)
		// Closed loop has no predetermined phase duration. Give each new
		// send its own response window rather than expiring a healthy long run.
		if c.Mode == "closed" {
			if err := conn.SetDeadline(time.Now().Add(c.Timeout)); err != nil {
				return fail(err)
			}
		}
		actual := time.Now()
		lag := actual.Sub(intended).Nanoseconds()
		if lag > result.MaxSendLag {
			result.MaxSendLag = lag
		}
		oid := prefix + "-" + strconv.Itoa(i)
		pending.Store(oid, sendStamp{intended, actual})
		diagnostics.Event("client_send", oid)
		side := protobuf.OrderSide_BUY
		if i%2 != 0 {
			side = protobuf.OrderSide_SELL
		}
		// Default scale 2: $3000.00 = 300_000 minor units, not 3_000_000_000.
		order := &protobuf.NewOrderSingle{OrderId: oid, Symbol: symbol, Price: price, Quantity: c.Quantity, Side: side, OrderType: protobuf.OrderType_LIMIT, TimeInForce: protobuf.TimeInForce_GTC, StpMode: protobuf.SelfTradePrevention_SELF_TRADE_PREVENTION_NONE}
		if err = sendFramed(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_NewOrder{NewOrder: order}}); err != nil {
			return fail(err)
		}
		if c.Mode == "closed" {
			select {
			case <-acknowledged:
			case err = <-received:
				if err == nil && i == count-1 {
					return result, nil
				}
				_ = conn.Close()
				return result, fmt.Errorf("closed-loop reader terminated early: %v", err)
			}
		}
	}
	if err := <-received; err != nil {
		_ = conn.Close()
		return result, err
	}
	return result, nil
}

type quantiles struct {
	Count int64         `json:"count"`
	P50   int64         `json:"p50_ns"`
	P90   int64         `json:"p90_ns"`
	P99   int64         `json:"p99_ns"`
	P999  int64         `json:"p99_9_ns"`
	Max   int64         `json:"max_ns"`
	HDR   *hdr.Snapshot `json:"hdr"`
}

func summarize(h *hdr.Histogram) quantiles {
	return quantiles{h.TotalCount(), h.ValueAtQuantile(50), h.ValueAtQuantile(90), h.ValueAtQuantile(99), h.ValueAtQuantile(99.9), h.Max(), h.Export()}
}

type repetition struct {
	MarketDataUpdates int64     `json:"market_data_updates_received"`
	Index             int       `json:"index"`
	Orders            int64     `json:"orders"`
	ElapsedNS         int64     `json:"elapsed_ns"`
	OrdersPerSecond   float64   `json:"orders_per_second"`
	MaxSendLag        int64     `json:"max_send_lag_ns"`
	Ack               quantiles `json:"ack"`
	SendLag           quantiles `json:"send_lag"`
	AckAfterSend      quantiles `json:"ack_after_send"`
	Fill              quantiles `json:"fill"`
}

func runRepetition(c config, index int) (repetition, error) {
	nonce := fmt.Sprintf("%d-%d", time.Now().UnixNano(), index)
	var mdUpdates atomic.Int64
	var subscribers []net.Conn
	var mdReaders sync.WaitGroup
	mdErrors := make(chan error, c.Subscribers)
	var closing atomic.Bool
	defer func() {
		closing.Store(true)
		for _, conn := range subscribers {
			_ = conn.Close()
		}
		mdReaders.Wait()
	}()

	conns := make([]net.Conn, 0, c.Concurrency)
	defer func() {
		for _, conn := range conns {
			_ = conn.Close()
		}
	}()
	for id := 0; id < c.Concurrency; id++ {
		conn, err := connect(c, id, nonce)
		if err != nil {
			return repetition{}, err
		}
		conns = append(conns, conn)
	}
	run := func(label string, count int) (phaseResult, time.Duration, error) {
		start := time.Now().Add(20 * time.Millisecond)
		results := make(chan struct {
			r   phaseResult
			err error
		}, len(conns))
		for id, conn := range conns {
			go func(id int, conn net.Conn) {
				r, err := runClientPhase(conn, c, symbolFor(c, id), fmt.Sprintf("%s-%s-%d", nonce, label, id), start, count)
				results <- struct {
					r   phaseResult
					err error
				}{r, err}
			}(id, conn)
		}
		combined := phaseResult{Ack: histogram(), Fill: histogram(), SendLag: histogram(), AckAfterSend: histogram()}
		var first error
		for range conns {
			v := <-results
			if v.err != nil && first == nil {
				first = v.err
				for _, conn := range conns {
					_ = conn.Close()
				}
			}
			if v.r.Ack != nil && v.r.Fill != nil {
				if combined.Ack.Merge(v.r.Ack) > 0 || combined.Fill.Merge(v.r.Fill) > 0 || combined.SendLag.Merge(v.r.SendLag) > 0 || combined.AckAfterSend.Merge(v.r.AckAfterSend) > 0 {
					first = errors.New("HDR merge dropped samples")
				}
				if v.r.MaxSendLag > combined.MaxSendLag {
					combined.MaxSendLag = v.r.MaxSendLag
				}
			}
		}
		return combined, time.Since(start), first
	}
	if _, _, err := run("warmup", c.Warmup); err != nil {
		return repetition{}, fmt.Errorf("warmup: %w", err)
	}
	for i := 0; i < c.Subscribers; i++ {
		conn, err := connect(c, 10000+i, nonce)
		if err != nil {
			return repetition{}, err
		}
		subscribers = append(subscribers, conn)
		symbols := []string{}
		seen := map[string]bool{}
		for id := 0; id < c.Concurrency; id++ {
			symbol := symbolFor(c, id)
			if !seen[symbol] {
				seen[symbol] = true
				symbols = append(symbols, symbol)
			}
		}
		if err = sendFramed(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_MarketDataRequest{MarketDataRequest: &protobuf.MarketDataRequest{Symbols: symbols}}}); err != nil {
			return repetition{}, err
		}
		// The completed warmup creates every symbol. Wait for each subscription
		// snapshot before measuring, otherwise the first repetition can have no
		// subscribers while subsequent repetitions fan out.
		for range symbols {
			snapshot, err := readFramed(conn)
			if err != nil {
				return repetition{}, err
			}
			if snapshot.GetMarketDataSnapshot() == nil {
				return repetition{}, errors.New("expected subscription snapshot")
			}
		}
		_ = conn.SetDeadline(time.Time{})
		mdReaders.Add(1)
		go func() {
			defer mdReaders.Done()
			for {
				m, err := readFramed(conn)
				if err != nil {
					if !closing.Load() {
						mdErrors <- err
					}
					return
				}
				if m.GetMarketDataIncremental() != nil {
					mdUpdates.Add(1)
				}
				if m.GetErrorMessage() != nil {
					mdErrors <- errors.New(m.GetErrorMessage().ErrorMessage)
					return
				}
			}
		}()
	}

	r, elapsed, err := run("measured", c.Orders)
	if err != nil {
		return repetition{}, err
	}
	orders := int64(c.Concurrency) * int64(c.Orders)
	if r.Ack.TotalCount() != orders || r.Fill.TotalCount() != orders {
		return repetition{}, errors.New("incomplete histogram counts")
	}
	select {
	case err := <-mdErrors:
		return repetition{}, fmt.Errorf("market-data reader: %w", err)
	default:
	}
	return repetition{mdUpdates.Load(), index, orders, elapsed.Nanoseconds(), float64(orders) / elapsed.Seconds(), r.MaxSendLag, summarize(r.Ack), summarize(r.SendLag), summarize(r.AckAfterSend), summarize(r.Fill)}, nil
}

type statistic struct {
	Median float64 `json:"median"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

func spread(values []float64) statistic {
	v := append([]float64(nil), values...)
	sort.Float64s(v)
	if len(v) == 0 {
		return statistic{}
	}
	middle := len(v) / 2
	median := v[middle]
	if len(v)%2 == 0 {
		median = (v[middle-1] + median) / 2
	}
	return statistic{median, v[0], v[len(v)-1]}
}
func aggregate(runs []repetition) map[string]statistic {
	values := map[string][]float64{}
	for _, r := range runs {
		values["orders_per_second"] = append(values["orders_per_second"], r.OrdersPerSecond)
		values["max_send_lag_ns"] = append(values["max_send_lag_ns"], float64(r.MaxSendLag))
		for name, q := range map[string]quantiles{"ack": r.Ack, "fill": r.Fill, "send_lag": r.SendLag, "ack_after_send": r.AckAfterSend} {
			for metric, v := range map[string]int64{"p50_ns": q.P50, "p90_ns": q.P90, "p99_ns": q.P99, "p99_9_ns": q.P999, "max_ns": q.Max} {
				key := name + "_" + metric
				values[key] = append(values[key], float64(v))
			}
		}
	}
	out := map[string]statistic{}
	for k, v := range values {
		out[k] = spread(v)
	}
	return out
}

type instrument struct {
	Symbol     string             `json:"symbol"`
	PriceMinor int64              `json:"price_minor_units"`
	Scale      int                `json:"price_scale"`
	Rules      symbolconfig.Rules `json:"rules"`
}

func instruments(c config) []instrument {
	seen := map[string]bool{}
	out := []instrument{}
	for id := 0; id < c.Concurrency; id++ {
		s := symbolFor(c, id)
		if seen[s] {
			continue
		}
		seen[s] = true
		r := symbolconfig.Current().For(s)
		p, _ := symbolconfig.Current().ParsePrice(s, "3000.00")
		out = append(out, instrument{s, p, r.PriceScale, r})
	}
	return out
}
func environment(c config) map[string]string {
	env := map[string]string{"go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "logical_cpus": strconv.Itoa(runtime.NumCPU()), "gomaxprocs": strconv.Itoa(runtime.GOMAXPROCS(0)), "pinning_notes": c.Pinning, "rustc_local": "unavailable (remote core compiler is unknown)"}
	for _, key := range []string{"FXP_GW_RATE", "FXP_GW_RATE_BURST", "FXP_SYMBOL_CONFIG", "TOKIO_WORKER_THREADS", "RUST_LOG"} {
		env["launch_env_"+key] = os.Getenv(key)
	}
	if b, err := exec.Command("rustc", "--version").Output(); err == nil {
		env["rustc_local"] = strings.TrimSpace(string(b))
	}
	if b, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		env["revision"] = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") {
				_, v, _ := strings.Cut(line, ":")
				env["cpu"] = strings.TrimSpace(v)
				break
			}
		}
	}
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "Cpus_allowed_list:") {
				env["client_cpu_affinity"] = strings.TrimSpace(strings.TrimPrefix(line, "Cpus_allowed_list:"))
			}
		}
	}
	return env
}
func markdown(c config, env map[string]string, summary map[string]statistic) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Live FXP benchmark\n\nMode: **%s**; %d connections; %d measured + %d warmup orders/connection; %d repetitions; open-loop rate %d orders/s/connection (ignored in closed mode).\n\n", c.Mode, c.Concurrency, c.Orders, c.Warmup, c.Repetitions, c.Rate)
	fmt.Fprintf(&b, "Transport: TLS=%t; client certificate=%t.\n\n", c.TLS.Enabled, c.TLS.CertFile != "")
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "- %s: %s\n", k, env[k])
	}
	b.WriteString("\n| Metric | Median of runs | Min | Max |\n|---|---:|---:|---:|\n")
	for _, metric := range []string{"orders_per_second", "max_send_lag_ns", "ack_p50_ns", "ack_p90_ns", "ack_p99_ns", "ack_p99_9_ns", "ack_max_ns", "fill_p50_ns", "fill_p90_ns", "fill_p99_ns", "fill_p99_9_ns", "fill_max_ns"} {
		v := summary[metric]
		fmt.Fprintf(&b, "| %s | %.1f | %.1f | %.1f |\n", metric, v.Median, v.Min, v.Max)
	}
	b.WriteString("\nLatency units: ns, HDR 3 significant digits. Both endpoints include sender backlog in open mode. ACK = EXEC_NEW; fill = terminal ORDER_FILLED, not every report. Throughput counts orders once and includes final fill drain. Warmup is a separate completed phase. Runs with rejects, missing responses or dropped samples fail.\n")
	return b.String()
}
func run() error {
	stopDiagnostics, err := diagnostics.Start()
	if err != nil {
		return err
	}
	defer stopDiagnostics()
	options, err := tlsconfig.ClientFromEnv("FXP_CLIENT_")
	if err != nil {
		return err
	}
	options.Flags(flag.CommandLine)
	c := config{}
	var jsonPath, markdownPath string
	var qty int
	flag.IntVar(&c.Subscribers, "subscribers", 0, "additional authenticated market-data readers; zero disables fan-out fixture")
	flag.StringVar(&c.Pacer, "pacer", "sleep", "open-loop pacing: sleep, busy-spin or hybrid (100us spin window)")
	flag.StringVar(&c.Mode, "mode", "open", "open: fixed intended schedule; closed: wait for each ACK")
	flag.StringVar(&c.Address, "address", "127.0.0.1:8082", "TCP gateway")
	flag.IntVar(&c.Concurrency, "concurrency", 1, "concurrent sessions")
	flag.IntVar(&c.Orders, "orders", 200, "even measured orders per session")
	flag.IntVar(&c.Warmup, "warmup", 20, "even separate warmup orders per session")
	flag.IntVar(&c.Repetitions, "repetitions", 5, "independent repetitions (median and min/max)")
	flag.IntVar(&c.Rate, "rate", 1000, "open-loop intended orders/s/session; ignored in closed mode")
	flag.DurationVar(&c.Timeout, "timeout", 30*time.Second, "response drain timeout")
	flag.StringVar(&c.Symbol, "symbol", "BENCH", "base symbol; isolated fresh stack required")
	flag.StringVar(&c.Symbols, "symbols", "single", "single or spread (adjacent session pairs share a symbol)")
	flag.IntVar(&qty, "quantity", 1, "quantity respecting symbol lot/min/max")
	flag.StringVar(&c.Pinning, "pinning", "not explicitly pinned; core/gateway affinity not verified", "actual CPU pinning description")
	flag.StringVar(&jsonPath, "json", "benchmark-results.json", "JSON output including per-run HDR snapshots")
	flag.StringVar(&markdownPath, "markdown", "benchmark-results.md", "Markdown output")
	flag.Parse()
	c.TLS = options
	if _, err := options.Config(); err != nil {
		return err
	}
	if qty < 1 || int64(qty) > int64(1<<31-1) {
		return errors.New("quantity outside int32 range")
	}
	c.Quantity = int32(qty)
	c.Price = 300_000
	if err := symbolconfig.Configure(os.Getenv("FXP_SYMBOL_CONFIG")); err != nil {
		return err
	}
	if err := validate(c); err != nil {
		return err
	}
	env := environment(c)
	runs := make([]repetition, 0, c.Repetitions)
	var failure error
	for i := 1; i <= c.Repetitions; i++ {
		r, err := runRepetition(c, i)
		if err != nil {
			failure = fmt.Errorf("repetition %d: %w", i, err)
			break
		}
		runs = append(runs, r)
	}
	report := struct {
		SchemaVersion int                  `json:"schema_version"`
		Created       string               `json:"created_utc"`
		Config        config               `json:"config"`
		Instruments   []instrument         `json:"instruments"`
		Environment   map[string]string    `json:"environment"`
		Runs          []repetition         `json:"runs"`
		Summary       map[string]statistic `json:"summary"`
		Error         string               `json:"error,omitempty"`
	}{SchemaVersion: 1, Created: time.Now().UTC().Format(time.RFC3339), Config: c, Instruments: instruments(c), Environment: env, Runs: runs}
	if failure == nil {
		report.Summary = aggregate(runs)
	} else {
		report.Error = failure.Error()
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(jsonPath, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if failure != nil {
		if err := os.WriteFile(markdownPath, []byte("# Benchmark failed\n\n"+failure.Error()+"\n"), 0o644); err != nil {
			return err
		}
		return failure
	}
	md := markdown(c, env, report.Summary)
	if err = os.WriteFile(markdownPath, []byte(md), 0o644); err != nil {
		return err
	}
	fmt.Print(md)
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "benchmark failed:", err)
		os.Exit(1)
	}
}
