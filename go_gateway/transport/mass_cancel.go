// SPDX-License-Identifier: MIT
package transport

import (
	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
	"log"
	"time"
)

func massCancelTimeout() time.Duration {
	seconds := envInt("FXP_GW_MASS_CANCEL_TIMEOUT_SEC", 30)
	if seconds > 86400 {
		log.Print("FXP_GW_MASS_CANCEL_TIMEOUT_SEC exceeds 86400; using 30")
		seconds = 30
	}
	return time.Duration(seconds) * time.Second
}
func registerMassCancelRoute(conn any, req *protobuf.OrderMassCancelRequest, timeout time.Duration) (string, error) {
	// Keep immutable request metadata; neither the conn nor caller's mutable proto
	// is captured by the response factory/timer callback.
	owner, id, symbol, side, reason := req.Sender, req.ClientRequestId, req.Symbol, req.Side, req.Reason
	reject := func(text string) *protobuf.FXPMessage {
		return &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderMassCancelReport{OrderMassCancelReport: &protobuf.OrderMassCancelReport{Sender: owner, ClientRequestId: id, Symbol: symbol, Side: side, Reason: reason, RejectReason: text}}}
	}
	return registerPending(&pendingRequest{conn: conn, owner: owner, original: id, kind: "mass cancel", reject: reject}, timeout)
}
func forwardMassCancelRequest(conn any, msg *protobuf.FXPMessage) {
	req := msg.GetOrderMassCancel()
	key, err := registerMassCancelRoute(conn, req, massCancelTimeout())
	if err != nil {
		_ = writeFXPMessage(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderMassCancelReport{OrderMassCancelReport: &protobuf.OrderMassCancelReport{Sender: req.Sender, ClientRequestId: req.ClientRequestId, Symbol: req.Symbol, Side: req.Side, Reason: req.Reason, RejectReason: err.Error()}}})
		return
	}
	cloned := proto.Clone(msg).(*protobuf.FXPMessage)
	cloned.GetOrderMassCancel().ClientRequestId = key
	raw, err := proto.Marshal(cloned)
	if err == nil {
		err = writeToCore(frameBytes(raw))
	}
	if err != nil {
		if r := pendingRequests.get(key, req.Sender, true, "mass cancel"); r != nil {
			_ = writeFXPMessage(r.conn, r.reject("core unavailable"))
		}
	}
}
func routeMassCancelResponse(msg *protobuf.FXPMessage) {
	rep := msg.GetOrderMassCancelReport()
	r := pendingRequests.get(rep.ClientRequestId, rep.Sender, true, "mass cancel")
	if r == nil {
		return
	} // includes COD requests and late/foreign responses
	cloned := proto.Clone(msg).(*protobuf.FXPMessage)
	cloned.GetOrderMassCancelReport().ClientRequestId = r.original
	_ = writeFXPMessage(r.conn, cloned)
}
