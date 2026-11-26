// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"slices"
	"sync"
)

// mdRequest is one MarketDataRequest(V) for one symbol.
type mdRequest struct {
	id           string // MDReqID(262)
	stream       bool   // 263=1: incremental updates follow the snapshot
	needSnapshot bool
}

// mdSubscriptions routes market data to the client's MarketDataRequests.
// Upstream data is per symbol and the gateway rebroadcasts every subscriber's
// snapshot, so delivery is filtered here: each request gets one snapshot and
// then incrementals only if it asked for updates (263=1).
type mdSubscriptions struct {
	mu      sync.Mutex
	bySym   map[string][]mdRequest
	pending map[string]mdPending
	serial  uint64
}

type mdPending struct {
	id      string
	stream  bool
	symbols []string
}

// prepare waits for gateway admission before changing live FIX routing.
func (s *mdSubscriptions) prepare(id string, stream bool, symbols []string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = make(map[string]mdPending)
	}
	if len(s.pending) >= 256 {
		return "", fmt.Errorf("too many pending market-data requests")
	}
	s.serial++
	key := fmt.Sprintf("fix-md-%d", s.serial)
	s.pending[key] = mdPending{id: id, stream: stream, symbols: slices.Clone(symbols)}
	return key, nil
}

func (s *mdSubscriptions) admit(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req, ok := s.pending[key]; ok {
		delete(s.pending, key)
		s.subscribeLocked(req.id, req.stream, req.symbols)
	}
}

func (s *mdSubscriptions) reject(key string) (mdPending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.pending[key]
	delete(s.pending, key)
	return req, ok
}

func (s *mdSubscriptions) unusedLocked(symbols []string) []string {
	var unused []string
	for _, symbol := range symbols {
		if len(s.bySym[symbol]) != 0 {
			continue
		}
		pending := false
		for _, req := range s.pending {
			if slices.Contains(req.symbols, symbol) {
				pending = true
				break
			}
		}
		if !pending {
			unused = append(unused, symbol)
		}
	}
	return unused
}

// subscribe records a request for symbols, replacing any earlier request with
// the same MDReqID. undo restores the previous state if forwarding fails.
func (s *mdSubscriptions) subscribe(reqID string, stream bool, symbols []string) (undo func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subscribeLocked(reqID, stream, symbols)
}

func (s *mdSubscriptions) subscribeLocked(reqID string, stream bool, symbols []string) (undo func()) {
	if s.bySym == nil {
		s.bySym = map[string][]mdRequest{}
	}
	prev := make(map[string][]mdRequest, len(symbols))
	for _, sym := range symbols {
		if _, saved := prev[sym]; !saved {
			prev[sym] = slices.Clone(s.bySym[sym])
		}
		reqs := slices.DeleteFunc(slices.Clone(s.bySym[sym]), func(r mdRequest) bool { return r.id == reqID })
		s.bySym[sym] = append(reqs, mdRequest{id: reqID, stream: stream, needSnapshot: true})
	}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for sym, reqs := range prev {
			s.setLocked(sym, reqs)
		}
	}
}

// unsubscribe removes the request with reqID from symbols (263=2), or from
// every symbol if none are given.
func (s *mdSubscriptions) unsubscribe(reqID string, symbols []string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed []string
	// Pending admissions must also honor an unsubscribe that arrives before ack.
	for key, req := range s.pending {
		if req.id != reqID {
			continue
		}
		kept := slices.DeleteFunc(slices.Clone(req.symbols), func(sym string) bool {
			if len(symbols) == 0 || slices.Contains(symbols, sym) {
				removed = append(removed, sym)
				return true
			}
			return false
		})
		if len(kept) == 0 {
			delete(s.pending, key)
		} else {
			req.symbols = kept
			s.pending[key] = req
		}
	}
	for sym, reqs := range s.bySym {
		if len(symbols) > 0 && !slices.Contains(symbols, sym) {
			continue
		}
		s.setLocked(sym, slices.DeleteFunc(reqs, func(r mdRequest) bool { return r.id == reqID }))
		removed = append(removed, sym)
	}
	return removed
}

// snapshotTargets returns the requests still owed a snapshot of symbol and
// marks them served; snapshot-only requests are then complete and removed.
func (s *mdSubscriptions) snapshotTargets(symbol string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	kept := s.bySym[symbol][:0]
	for _, r := range s.bySym[symbol] {
		if r.needSnapshot {
			ids = append(ids, r.id)
			r.needSnapshot = false
			if !r.stream {
				continue
			}
		}
		kept = append(kept, r)
	}
	s.setLocked(symbol, kept)
	return ids
}

// incrementalTargets returns the streaming requests that have had their snapshot.
func (s *mdSubscriptions) incrementalTargets(symbol string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, r := range s.bySym[symbol] {
		if r.stream && !r.needSnapshot {
			ids = append(ids, r.id)
		}
	}
	return ids
}

func (s *mdSubscriptions) setLocked(symbol string, reqs []mdRequest) {
	if len(reqs) == 0 {
		delete(s.bySym, symbol)
		return
	}
	s.bySym[symbol] = reqs
}
