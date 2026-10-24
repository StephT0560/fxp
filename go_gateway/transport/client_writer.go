// SPDX-License-Identifier: MIT

package transport

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"

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
	finished  chan struct{}
	stopOnce  sync.Once
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
		finished: make(chan struct{}),
	}
	if old, loaded := clientWriters.Swap(conn, writer); loaded {
		old.(*clientWriter).stop()
	}
	go writer.run()
	return writer
}

func unregisterClientWriter(conn any) {
	if value, ok := clientWriters.LoadAndDelete(conn); ok {
		value.(*clientWriter).stop()
	}
}

func (w *clientWriter) stop() {
	w.stopOnce.Do(func() {
		close(w.stopped)
		closeConn(w.conn)
	})
}

func (w *clientWriter) fail(err error) {
	if err != nil {
		log.Printf("client writer failed (%T): %v", w.conn, err)
	}
	w.stop()
	clientWriters.CompareAndDelete(w.conn, w)
}

func (w *clientWriter) enqueue(raw []byte, marketData bool) error {
	data := append([]byte(nil), raw...)
	select {
	case <-w.stopped:
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
	for {
		select {
		case <-w.stopped:
			return
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
	switch conn := w.conn.(type) {
	case net.Conn:
		frame := make([]byte, 4+len(raw))
		binary.BigEndian.PutUint32(frame[:4], uint32(len(raw)))
		copy(frame[4:], raw)
		for len(frame) > 0 {
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
