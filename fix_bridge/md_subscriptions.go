// SPDX-License-Identifier: MIT

package main

import (
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
	mu    sync.Mutex
	bySym map[string][]mdRequest
}

// subscribe records a request for symbols, replacing any earlier request with
// the same MDReqID. undo restores the previous state if forwarding fails.
func (s *mdSubscriptions) subscribe(reqID string, stream bool, symbols []string) (undo func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// unsubscribe removes the request with reqID from every symbol (263=2).
func (s *mdSubscriptions) unsubscribe(reqID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sym, reqs := range s.bySym {
		s.setLocked(sym, slices.DeleteFunc(reqs, func(r mdRequest) bool { return r.id == reqID }))
	}
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
