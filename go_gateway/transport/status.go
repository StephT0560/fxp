// SPDX-License-Identifier: MIT
package transport

import (
	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
	"time"
)

func statusReject(owner, id, reason string) *protobuf.FXPMessage {
	return &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderStatusResponse{OrderStatusResponse: &protobuf.OrderStatusResponse{RequestId: id, Sender: owner, RejectReason: reason, Complete: true}}}
}
func registerStatusRoute(conn any, req *protobuf.OrderStatusRequest) (string, error) {
	owner, id := req.Sender, req.RequestId
	return registerPending(&pendingRequest{conn: conn, owner: owner, original: id, kind: "order status", reject: func(reason string) *protobuf.FXPMessage { return statusReject(owner, id, reason) }}, 30*time.Second)
}
func forwardStatusRequest(conn any, msg *protobuf.FXPMessage) {
	req := msg.GetOrderStatusRequest()
	key, err := registerStatusRoute(conn, req)
	if err != nil {
		_ = writeFXPMessage(conn, statusReject(req.Sender, req.RequestId, err.Error()))
		return
	}
	cloned := proto.Clone(msg).(*protobuf.FXPMessage)
	cloned.GetOrderStatusRequest().RequestId = key
	raw, err := proto.Marshal(cloned)
	if err == nil {
		err = writeToCore(frameBytes(raw))
	}
	if err != nil {
		if r := pendingRequests.get(key, req.Sender, true, "order status"); r != nil {
			_ = writeFXPMessage(r.conn, r.reject("core unavailable"))
		}
	}
}
func routeStatusResponse(msg *protobuf.FXPMessage) {
	rep := msg.GetOrderStatusResponse()
	r := pendingRequests.get(rep.RequestId, rep.Sender, rep.Complete || rep.RejectReason != "", "order status")
	if r == nil {
		return
	}
	cloned := proto.Clone(msg).(*protobuf.FXPMessage)
	out := cloned.GetOrderStatusResponse()
	out.RequestId = r.original
	for _, report := range out.Reports {
		// The requester adopts the owner's live orders, so a console that
		// reconnected (new socket) receives their later reports.
		if !terminalStatus(report.OrderStatus) && report.OrderId != "" {
			orderClientMap.Store(report.OrderId, orderRoute{conn: r.conn, owner: r.owner})
		}
		stripOwner(report, r.owner)
	}
	_ = writeFXPMessage(r.conn, cloned)
}

func terminalStatus(s protobuf.OrderStatus) bool {
	switch s {
	case protobuf.OrderStatus_ORDER_FILLED, protobuf.OrderStatus_ORDER_CANCELED, protobuf.OrderStatus_ORDER_REJECTED,
		protobuf.OrderStatus_ORDER_EXPIRED, protobuf.OrderStatus_ORDER_REPLACED:
		return true
	}
	return false
}
