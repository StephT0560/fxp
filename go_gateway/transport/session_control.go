// SPDX-License-Identifier: MIT
package transport

import (
	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
	"time"
)

func sessionControlReject(owner, id, reason string) *protobuf.FXPMessage {
	return &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_SessionControlReport{SessionControlReport: &protobuf.SessionControlReport{RequestId: id, Sender: owner, RejectReason: reason}}}
}
func registerSessionControlRoute(conn any, req *protobuf.SessionControl) (string, error) {
	owner, id := req.Sender, req.RequestId
	return registerPending(&pendingRequest{conn: conn, owner: owner, original: id, kind: "session control", reject: func(reason string) *protobuf.FXPMessage { return sessionControlReject(owner, id, reason) }}, 30*time.Second)
}
func forwardSessionControl(conn any, msg *protobuf.FXPMessage) {
	req := msg.GetSessionControl()
	key, err := registerSessionControlRoute(conn, req)
	if err != nil {
		_ = writeFXPMessage(conn, sessionControlReject(req.Sender, req.RequestId, err.Error()))
		return
	}
	cloned := proto.Clone(msg).(*protobuf.FXPMessage)
	cloned.GetSessionControl().RequestId = key
	raw, err := proto.Marshal(cloned)
	if err == nil {
		err = writeToCore(frameBytes(raw))
	}
	if err != nil {
		if r := pendingRequests.get(key, req.Sender, true, "session control"); r != nil {
			_ = writeFXPMessage(r.conn, r.reject("core unavailable"))
		}
	}
}
func routeSessionControlReport(msg *protobuf.FXPMessage) {
	rep := msg.GetSessionControlReport()
	r := pendingRequests.get(rep.RequestId, rep.Sender, true, "session control")
	if r == nil {
		return
	}
	cloned := proto.Clone(msg).(*protobuf.FXPMessage)
	cloned.GetSessionControlReport().RequestId = r.original
	_ = writeFXPMessage(r.conn, cloned)
}
