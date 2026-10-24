// SPDX-License-Identifier: MIT

package transport

// limits.go — connection hardening knobs (env-configurable) and helpers.

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

// Limits bounds what an unauthenticated or authenticated client can consume.
type Limits struct {
	MaxFramePreAuth  int           // FXP_GW_MAX_FRAME_PREAUTH (bytes) before Logon
	MaxFrame         int           // FXP_GW_MAX_FRAME (bytes) after Logon
	MaxConns         int           // FXP_GW_MAX_CONNS per listener (TCP and WS each)
	RatePerSec       float64       // FXP_GW_RATE messages/second per connection
	RateBurst        int           // FXP_GW_RATE_BURST
	HandshakeTimeout time.Duration // FXP_GW_HANDSHAKE_TIMEOUT_MS to complete Logon
	ClientQueue      int           // FXP_GW_CLIENT_QUEUE critical messages per client
	MDQueue          int           // FXP_GW_MD_QUEUE market-data messages per client
}

// Active limits and per-listener connection limiters. Servers and connections
// snapshot them when they start.
var (
	limitsP   atomic.Pointer[Limits]
	tcpConnsP atomic.Pointer[connLimiter]
	wsConnsP  atomic.Pointer[connLimiter]
)

func init() { setLimits(loadLimits()) }

func setLimits(l Limits) {
	limitsP.Store(&l)
	tcpConnsP.Store(newConnLimiter(l.MaxConns))
	wsConnsP.Store(newConnLimiter(l.MaxConns))
}

func currentLimits() Limits { return *limitsP.Load() }

func shutdownTimeout() time.Duration {
	return time.Duration(envInt("FXP_SHUTDOWN_TIMEOUT_SEC", 5)) * time.Second
}

func loadLimits() Limits {
	return Limits{
		MaxFramePreAuth:  envInt("FXP_GW_MAX_FRAME_PREAUTH", 4096),
		MaxFrame:         envInt("FXP_GW_MAX_FRAME", 65536),
		MaxConns:         envInt("FXP_GW_MAX_CONNS", 1024),
		RatePerSec:       float64(envInt("FXP_GW_RATE", 2000)),
		RateBurst:        envInt("FXP_GW_RATE_BURST", 4000),
		HandshakeTimeout: time.Duration(envInt("FXP_GW_HANDSHAKE_TIMEOUT_MS", 10000)) * time.Millisecond,
		ClientQueue:      envInt("FXP_GW_CLIENT_QUEUE", 8192),
		MDQueue:          envInt("FXP_GW_MD_QUEUE", 4096),
	}
}

func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Printf("⚠️ ignoring invalid %s=%q, using %d", name, v, def)
		return def
	}
	return n
}

// connLimiter caps concurrent connections.
type connLimiter struct{ slots chan struct{} }

func newConnLimiter(n int) *connLimiter { return &connLimiter{slots: make(chan struct{}, n)} }

func (l *connLimiter) tryAcquire() bool {
	select {
	case l.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *connLimiter) release() { <-l.slots }

// tokenBucket is a per-connection rate limiter (single goroutine, no lock).
type tokenBucket struct {
	rate, burst, tokens float64
	last                time.Time
}

func newTokenBucket(rate float64, burst int) *tokenBucket {
	return &tokenBucket{rate: rate, burst: float64(burst), tokens: float64(burst), last: time.Now()}
}

func (b *tokenBucket) allow(now time.Time) bool {
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// newSessionID returns a random server-generated session identifier.
func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// readFrame reads one 4-byte length-prefixed frame, rejecting lengths outside
// 1..max before allocating.
func readFrame(r io.Reader, max int) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > uint32(max) {
		return nil, fmt.Errorf("frame length %d outside 1..%d", n, max)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}
