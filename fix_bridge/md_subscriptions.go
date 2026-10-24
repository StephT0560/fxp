// SPDX-License-Identifier: MIT

package main

import (
	"sync"

	"fix_bridge/codec"
)

// mdSubscriptions maps each subscribed symbol to the MDReqID(262) of the
// MarketDataRequest that subscribed it, so W/X messages can echo it.
type mdSubscriptions struct {
	mu    sync.Mutex
	bySym map[string]string
}

// track records a MarketDataRequest. It returns false for an unsubscribe
// (263=2), which only removes the mapping and is not forwarded upstream.
func (s *mdSubscriptions) track(msg *codec.Message) bool {
	reqID := msg.Get(codec.TagMDReqID)
	subType := msg.Get(codec.TagSubscriptionRequestType)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bySym == nil {
		s.bySym = map[string]string{}
	}
	for _, f := range msg.Tags {
		if f.Tag != codec.TagSymbol {
			continue
		}
		switch subType {
		case "0", "1": // Snapshot, Snapshot + Updates
			s.bySym[f.Value] = reqID
		case "2": // Disable previous
			if s.bySym[f.Value] == reqID {
				delete(s.bySym, f.Value)
			}
		}
	}
	return subType != "2"
}

func (s *mdSubscriptions) reqID(symbol string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bySym[symbol]
}
