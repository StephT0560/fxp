// SPDX-License-Identifier: MIT

package transport

import (
	"encoding/binary"
	"errors"
	"fmt"
	"go_gateway/diagnostics"
	"io"
	"log"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var (
	clientWriters          sync.Map
	marketDataDroppedCount atomic.Uint64
	errClientQueueFull     = errors.New("critical client output queue full")
)

type clientWriter struct {
	conn      any
	critical  chan []byte
	md        chan []byte
	stopped   chan struct{}
	draining  chan struct{}
	finished  chan struct{}
	stopOnce  sync.Once
	drainOnce sync.Once
	enqueueMu sync.RWMutex
	mdDropped atomic.Uint64
}

func MarketDataDropped() uint64 { return marketDataDroppedCount.Load() }

func registerClientWriter(conn any) *clientWriter {
	lim := currentLimits()
	writer := &clientWriter{
		conn:     conn,
		critical: make(chan []byte, lim.ClientQueue),
		md:       make(chan []byte, lim.MDQueue),
		stopped:  make(chan struct{}),
		draining: make(chan struct{}),
		finished: make(chan struct{}),
	}
	if old, loaded := clientWriters.Swap(conn, writer); loaded {
		old.(*clientWriter).stop()
	}
	go writer.run()
	return writer
}

func unregisterClientWriter(conn any) {
	pendingRequests.clear(conn)
	clientSubs.Range(func(_, clients any) bool {
		clients.(*sync.Map).Delete(conn)
		return true
	})
	if value, ok := clientWriters.LoadAndDelete(conn); ok {
		value.(*clientWriter).stop()
	}
}

func shutdownClientWriters(timeout time.Duration) {
	var writers []*clientWriter
	clientWriters.Range(func(_, value any) bool {
		writer := value.(*clientWriter)
		writers = append(writers, writer)
		writer.drain()
		return true
	})
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for _, writer := range writers {
		select {
		case <-writer.finished:
		case <-timer.C:
			log.Printf("client writers did not stop within %v", timeout)
			for _, pending := range writers {
				pending.forceStop()
			}
			return
		}
	}
}

func (w *clientWriter) stop() {
	select {
	case <-w.draining:
		return
	default:
		w.forceStop()
	}
}

func (w *clientWriter) forceStop() {
	w.stopOnce.Do(func() {
		close(w.stopped)
		pendingRequests.clear(w.conn)
		closeConn(w.conn)
	})
}

func (w *clientWriter) drain() {
	w.drainOnce.Do(func() {
		w.enqueueMu.Lock()
		close(w.draining)
		switch conn := w.conn.(type) {
		case net.Conn:
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.CloseRead()
			} else {
				_ = conn.SetReadDeadline(time.Now())
			}
		case *websocket.Conn:
			_ = conn.SetReadDeadline(time.Now())
		}
		w.enqueueMu.Unlock()
	})
}

func (w *clientWriter) fail(err error) {
	if err != nil {
		log.Printf("client writer failed (%T): %v", w.conn, err)
	}
	w.forceStop()
	clientWriters.CompareAndDelete(w.conn, w)
}

func (w *clientWriter) enqueue(raw []byte, marketData bool) error {
	w.enqueueMu.RLock()
	defer w.enqueueMu.RUnlock()
	if phase1Enabled && marketData && phase1Incremental(raw) {
		phase1Count("locks")
	}

	data := append([]byte(nil), raw...)
	select {
	case <-w.stopped:
		return net.ErrClosed
	case <-w.draining:
		return net.ErrClosed
	default:
	}

	queue := w.critical
	if marketData {
		queue = w.md
	}
	select {
	case <-w.stopped:
		return net.ErrClosed
	case <-w.draining:
		return net.ErrClosed
	case queue <- data:
		return nil
	default:
		if marketData {
			w.mdDropped.Add(1)
			marketDataDroppedCount.Add(1)
			return nil
		}
		w.fail(errClientQueueFull)
		return errClientQueueFull
	}
}

func (w *clientWriter) run() {
	defer close(w.finished)
	draining := false
	for {
		if draining {
			select {
			case raw := <-w.critical:
				if err := w.write(raw); err != nil {
					w.fail(err)
					return
				}
			case raw := <-w.md:
				if err := w.write(raw); err != nil {
					w.fail(err)
					return
				}
			default:
				pendingRequests.clear(w.conn)
				closeConn(w.conn)
				clientWriters.CompareAndDelete(w.conn, w)
				return
			}
			continue
		}
		select {
		case <-w.stopped:
			return
		case <-w.draining:
			draining = true
			continue
		case raw := <-w.critical:
			if err := w.write(raw); err != nil {
				w.fail(err)
				return
			}
			continue
		default:
		}
		select {
		case <-w.stopped:
			return
		case <-w.draining:
			draining = true
		case raw := <-w.critical:
			if err := w.write(raw); err != nil {
				w.fail(err)
				return
			}
		case raw := <-w.md:
			if err := w.write(raw); err != nil {
				w.fail(err)
				return
			}
		}
	}
}

func (w *clientWriter) write(raw []byte) error {
	diagnostics.Raw("gateway_client_write", raw)
	switch conn := w.conn.(type) {
	case net.Conn:
		frame := make([]byte, 4+len(raw))
		binary.BigEndian.PutUint32(frame[:4], uint32(len(raw)))
		copy(frame[4:], raw)
		for len(frame) > 0 {
			if phase1Enabled && phase1Incremental(raw) {
				phase1Count("writes")
			}

			n, err := conn.Write(frame)
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
			frame = frame[n:]
		}
		return nil
	case *websocket.Conn:
		return conn.WriteMessage(websocket.BinaryMessage, raw)
	default:
		return fmt.Errorf("unsupported connection type: %T", w.conn)
	}
}

func enqueueClientData(conn any, raw []byte, marketData bool) error {
	value, ok := clientWriters.Load(conn)
	if !ok {
		slog.Debug("dropping output for unregistered client", "market_data", marketData)
		return net.ErrClosed
	}
	return value.(*clientWriter).enqueue(raw, marketData)
}
