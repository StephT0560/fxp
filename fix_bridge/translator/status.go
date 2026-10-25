// SPDX-License-Identifier: MIT
package translator

import (
	"bytes"
	"fix_bridge/codec"
	"fmt"
	"go_gateway/protobuf"
)

func translateStatusRequest(m *codec.Message) (*protobuf.FXPMessage, error) {
	req := &protobuf.OrderStatusRequest{Sender: m.Get(49), Symbol: m.Get(55)}
	if m.Get(54) != "" {
		side, e := mapSide(m.Get(54))
		if e != nil {
			return nil, e
		}
		req.Side = side
	}
	if m.MsgType() == "H" {
		req.OrderId = m.Get(11)
		if req.OrderId == "" {
			req.OrderId = m.Get(37)
		}
		if req.OrderId == "" {
			return nil, fmt.Errorf("OrderStatusRequest requires ClOrdID or OrderID")
		}
		req.RequestId = m.Get(790)
		if req.RequestId == "" {
			req.RequestId = req.OrderId
		}
	} else {
		req.RequestId = m.Get(584)
		switch m.Get(585) {
		case "1":
			if req.Symbol == "" {
				return nil, fmt.Errorf("MassStatusReqType=1 requires Symbol")
			}
		case "7":
			req.Symbol = ""
		default:
			return nil, fmt.Errorf("unsupported MassStatusReqType; supported: 1 (symbol), 7 (all)")
		}
	}
	return &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderStatusRequest{OrderStatusRequest: req}}, nil
}

// StatusExecutionReport produces FIX ExecType I without rebinding an order's
// live stream. Mass status includes total and final-report markers.
func StatusExecutionReport(rep *protobuf.ExecutionReport, sender, target string, seq int, id string, mass, complete bool, total uint32) []byte {
	raw := translateExecutionReport(rep, sender, target, seq)
	if raw == nil {
		return nil
	}
	m, err := codec.NewParser(bytes.NewReader(raw)).ReadMessage()
	if err != nil {
		return nil
	}
	b := codec.NewBuilder("8", sender, target, seq)
	for _, f := range m.Tags {
		switch f.Tag {
		case 8, 9, 10, 35, 49, 56, 34, 52:
			continue
		}
		b.Append(f.Tag, f.Value)
	}
	b.Set(17, "0")
	b.Set(150, "I")
	if mass {
		b.Set(584, id)
		b.SetInt(911, int(total))
		if complete {
			b.Set(912, "Y")
		} else {
			b.Set(912, "N")
		}
	} else {
		b.Set(790, id)
	}
	return b.Build()
}
