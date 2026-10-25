// SPDX-License-Identifier: MIT
package main

import (
	"fix_bridge/codec"
	"fix_bridge/translator"
	"fmt"
	"go_gateway/protobuf"
)

type statusRequest struct {
	mass                  bool
	id                    string
	refSeq                int
	msgType               string
	orderID, symbol, side string
}

func (h *bridgeHandler) trackStatus(m *codec.Message, q *protobuf.OrderStatusRequest) {
	r := statusRequest{mass: m.MsgType() == "AF", id: q.RequestId, refSeq: m.GetInt(34), msgType: m.MsgType()}
	r.orderID = m.Get(11)
	if r.orderID == "" {
		r.orderID = m.Get(37)
	}
	r.symbol = m.Get(55)
	r.side = m.Get(54)
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
	if rep.Complete || rep.RejectReason != "" || len(rep.Reports) == 0 {
		h.statusPending.Delete(rep.RequestId)
	}
	if rep.RejectReason != "" || len(rep.Reports) == 0 {
		text := rep.RejectReason
		if text == "" {
			text = "No orders"
		}
		if !r.mass {
			if rep.RejectReason == "" {
				text = "Unknown order"
			}
			h.sess.SendWithSeq(func(seq int) []byte {
				b := codec.NewBuilder("8", h.bridgeCompID, h.sess.TargetCompID, seq)
				b.Set(37, "NONE").Set(11, r.orderID).Set(790, r.id)
				b.Set(17, "0").Set(150, "I").Set(39, "8")
				b.Set(55, r.symbol).Set(54, r.side)
				b.Set(14, "0").Set(151, "0").Set(6, "0").Set(58, text)
				code := codec.OrdRejReasonOther
				if text == "Unknown order" {
					code = codec.OrdRejReasonUnknownOrder
				}
				b.Set(103, code)
				return b.Build()
			})
			return
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
