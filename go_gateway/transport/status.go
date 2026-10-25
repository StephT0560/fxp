// SPDX-License-Identifier: MIT
package transport

import (
	"fmt"
	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
	"sync"
	"sync/atomic"
	"time"
)

type statusRoute struct {
	conn            any
	owner, original string
	timer           *time.Timer
}

var statusRoutes struct {
	sync.Mutex
	pending map[string]*statusRoute
}
var statusSerial atomic.Uint64

func registerStatusRoute(conn any, req *protobuf.OrderStatusRequest) string {
	key := fmt.Sprintf("status-%d", statusSerial.Add(1))
	r := &statusRoute{conn: conn, owner: req.Sender, original: req.RequestId}
	statusRoutes.Lock()
	defer statusRoutes.Unlock()
	if statusRoutes.pending == nil {
		statusRoutes.pending = map[string]*statusRoute{}
	}
	statusRoutes.pending[key] = r
	r.timer = time.AfterFunc(30*time.Second, func() {
		statusRoutes.Lock()
		old := statusRoutes.pending[key]
		delete(statusRoutes.pending, key)
		statusRoutes.Unlock()
		if old != nil {
			_ = writeFXPMessage(old.conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderStatusResponse{OrderStatusResponse: &protobuf.OrderStatusResponse{RequestId: old.original, Sender: old.owner, RejectReason: "order status timeout", Complete: true}}})
		}
	})
	return key
}
func removeStatusRoute(key string) {
	statusRoutes.Lock()
	if r := statusRoutes.pending[key]; r != nil {
		r.timer.Stop()
		delete(statusRoutes.pending, key)
	}
	statusRoutes.Unlock()
}
func clearStatusRoutes(conn any) {
	statusRoutes.Lock()
	for key, r := range statusRoutes.pending {
		if r.conn == conn {
			r.timer.Stop()
			delete(statusRoutes.pending, key)
		}
	}
	statusRoutes.Unlock()
}
func forwardStatusRequest(conn any, msg *protobuf.FXPMessage) {
	req := msg.GetOrderStatusRequest()
	key := registerStatusRoute(conn, req)
	cloned := proto.Clone(msg).(*protobuf.FXPMessage)
	cloned.GetOrderStatusRequest().RequestId = key
	raw, err := proto.Marshal(cloned)
	if err == nil {
		err = writeToCore(frameBytes(raw))
	}
	if err != nil {
		removeStatusRoute(key)
		_ = writeFXPMessage(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderStatusResponse{OrderStatusResponse: &protobuf.OrderStatusResponse{RequestId: req.RequestId, Sender: req.Sender, RejectReason: "core unavailable", Complete: true}}})
	}
}
func routeStatusResponse(msg *protobuf.FXPMessage) {
	rep := msg.GetOrderStatusResponse()
	statusRoutes.Lock()
	r := statusRoutes.pending[rep.RequestId]
	if r == nil || r.owner != rep.Sender {
		statusRoutes.Unlock()
		return
	}
	if rep.Complete {
		r.timer.Stop()
		delete(statusRoutes.pending, rep.RequestId)
	}
	statusRoutes.Unlock()
	cloned := proto.Clone(msg).(*protobuf.FXPMessage)
	out := cloned.GetOrderStatusResponse()
	out.RequestId = r.original
	for _, report := range out.Reports {
		stripOwner(report, r.owner)
	}
	_ = writeFXPMessage(r.conn, cloned)
}
