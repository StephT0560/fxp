// SPDX-License-Identifier: MIT
package main

import (
	"fix_bridge/codec"
	"fix_bridge/translator"
	"fmt"
	"go_gateway/protobuf"
)

type statusRequest struct {
	mass    bool
	id      string
	refSeq  int
	msgType string
}

func (h *bridgeHandler) trackStatus(m *codec.Message, q *protobuf.OrderStatusRequest) {
	r := statusRequest{mass: m.MsgType() == "AF", id: q.RequestId, refSeq: m.GetInt(34), msgType: m.MsgType()}
	key := fmt.Sprintf("fix-status-%d", h.statusSerial.Add(1))
	q.RequestId = key
	h.statusPending.Store(key, r)
}
func (h *bridgeHandler) onStatus(rep *protobuf.OrderStatusResponse) {
	v, ok := h.statusPending.Load(rep.RequestId)
	if !ok {
		return
	}
	r := v.(statusRequest)
	if rep.Complete {
		h.statusPending.Delete(rep.RequestId)
	}
	if rep.RejectReason != "" || len(rep.Reports) == 0 {
		text := rep.RejectReason
		if text == "" {
			text = "No orders"
		}
		h.sess.SendWithSeq(func(seq int) []byte {
			b := codec.NewBuilder("j", h.bridgeCompID, h.sess.TargetCompID, seq)
			b.SetInt(45, r.refSeq)
			b.Set(372, r.msgType)
			b.Set(379, r.id)
			b.Set(380, "1")
			b.Set(58, text)
			return b.Build()
		})
		return
	}
	for i, report := range rep.Reports {
		last := rep.Complete && i == len(rep.Reports)-1
		h.sess.SendWithSeq(func(seq int) []byte {
			return translator.StatusExecutionReport(report, h.bridgeCompID, h.sess.TargetCompID, seq, r.id, r.mass, last, rep.TotalReports)
		})
	}
}
