// SPDX-License-Identifier: MIT
package transport

import (
	"errors"
	"fmt"
	"go_gateway/protobuf"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxPendingRequests = 4096

var errPendingFull = errors.New("too many pending requests")
var pendingRequests = newPendingTracker(maxPendingRequests)

type pendingRequest struct {
	conn                  any
	writer                *clientWriter
	owner, original, kind string
	reject                func(string) *protobuf.FXPMessage
	timer                 *time.Timer
}
type pendingTracker struct {
	sync.Mutex
	items  map[string]*pendingRequest
	serial atomic.Uint64
	limit  int
}

func newPendingTracker(limit int) *pendingTracker {
	return &pendingTracker{items: make(map[string]*pendingRequest), limit: limit}
}
func (t *pendingTracker) add(r *pendingRequest, timeout time.Duration) (string, error) {
	t.Lock()
	defer t.Unlock()
	if r.writer != nil {
		select {
		case <-r.writer.stopped:
			return "", errors.New("client closed")
		case <-r.writer.draining:
			return "", errors.New("client closed")
		default:
		}
	}
	if len(t.items) >= t.limit {
		return "", errPendingFull
	}
	key := fmt.Sprintf("%s-%d", strings.ReplaceAll(r.kind, " ", "-"), t.serial.Add(1))
	t.items[key] = r
	// Capture only the tracker/key: stopped timers must not retain connections.
	r.timer = time.AfterFunc(timeout, func() { t.expire(key) })
	return key, nil
}
func (t *pendingTracker) get(key, owner string, complete bool, kind ...string) *pendingRequest {
	t.Lock()
	defer t.Unlock()
	r := t.items[key]
	if r == nil || r.owner != owner || len(kind) > 0 && r.kind != kind[0] {
		return nil
	}
	if complete {
		r.timer.Stop()
		delete(t.items, key)
	}
	return r
}
func (t *pendingTracker) expire(key string) {
	t.Lock()
	r := t.items[key]
	delete(t.items, key)
	t.Unlock()
	if r != nil && r.reject != nil {
		_ = writeFXPMessage(r.conn, r.reject(r.kind+" timeout"))
	}
}
func (t *pendingTracker) clear(conn any) {
	t.Lock()
	defer t.Unlock()
	for key, r := range t.items {
		if r.conn == conn {
			r.timer.Stop()
			delete(t.items, key)
		}
	}
}
func (t *pendingTracker) count() int { t.Lock(); defer t.Unlock(); return len(t.items) }
func registerPending(r *pendingRequest, timeout time.Duration) (string, error) {
	value, ok := clientWriters.Load(r.conn)
	if !ok {
		return "", errors.New("client closed")
	}
	r.writer = value.(*clientWriter)
	return pendingRequests.add(r, timeout)
}
