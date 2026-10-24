// SPDX-License-Identifier: MIT

package main

import (
	"sync"

	"fix_bridge/codec"
	"go_gateway/protobuf"
)

// pendingRequests remembers forwarded cancel and cancel/replace requests by
// ClOrdID until their first ExecutionReport, so a core reject (which has no
// Side) can be answered with an OrderCancelReject instead of being dropped.
type pendingRequests struct {
	mu   sync.Mutex
	reqs map[string]*codec.Message
}

func (p *pendingRequests) add(req *codec.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reqs == nil {
		p.reqs = map[string]*codec.Message{}
	}
	p.reqs[req.Get(codec.TagClOrdID)] = req
}

func (p *pendingRequests) take(clOrdID string) *codec.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	req := p.reqs[clOrdID]
	delete(p.reqs, clOrdID)
	return req
}

// isCancelOrReplace reports whether req is an OrderCancelRequest or an
// OrderCancelReplaceRequest.
func isCancelOrReplace(req *codec.Message) bool {
	t := req.MsgType()
	return t == codec.MsgTypeOrderCancelRequest || t == codec.MsgTypeOrderCancelReplaceRequest
}

// cancelRequestReject returns the pending request rep rejects, or nil.
// Any report for a pending request clears it.
func (p *pendingRequests) cancelRequestReject(rep *protobuf.ExecutionReport) *codec.Message {
	if rep == nil || rep.ClientOrderId == "" {
		return nil
	}
	req := p.take(rep.ClientOrderId)
	if req == nil || rep.ExecutionType != protobuf.ExecutionType_EXEC_REJECTED {
		return nil
	}
	return req
}

func buildCancelReject(req *codec.Message, orderID, bridgeCompID, targetCompID string, seq int, reason string) []byte {
	if orderID == "" {
		orderID = "NONE"
	}
	b := codec.NewBuilder(codec.MsgTypeOrderCancelReject, bridgeCompID, targetCompID, seq)
	b.Set(codec.TagOrderID, orderID)
	b.Set(codec.TagClOrdID, req.Get(codec.TagClOrdID))
	b.Set(codec.TagOrigClOrdID, req.Get(codec.TagOrigClOrdID))
	b.Set(codec.TagOrdStatus, codec.OrdStatusRejected)
	b.Set(codec.TagCxlRejReason, "0") // Too late to cancel
	if req.MsgType() == codec.MsgTypeOrderCancelReplaceRequest {
		b.Set(codec.TagCxlRejResponseTo, "2")
	} else {
		b.Set(codec.TagCxlRejResponseTo, "1")
	}
	b.Set(codec.TagText, reason)
	return b.Build()
}
