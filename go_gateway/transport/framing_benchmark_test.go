// SPDX-License-Identifier: MIT
package transport

import (
	"bytes"
	"encoding/binary"
	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestOrderFrameMatchesProtoBytes(t *testing.T) {
	m := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_NewOrder{NewOrder: &protobuf.NewOrderSingle{OrderId: "OWNER::O1", Symbol: "BENCH", Price: 300000, Quantity: 1}}}
	frame, err := marshalOrderFrame(m)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := proto.Marshal(m)
	if int(binary.BigEndian.Uint32(frame)) != len(want) || !bytes.Equal(frame[4:], want) {
		t.Fatal("framing or protobuf bytes changed")
	}
}

var frameBenchmarkSink []byte

func BenchmarkOrderFrame(b *testing.B) {
	m := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_NewOrder{NewOrder: &protobuf.NewOrderSingle{OrderId: "OWNER::ORDER-1234567890", Symbol: "BENCH", Price: 300000, Quantity: 1}}}
	b.ReportAllocs()
	for b.Loop() {
		var err error
		frameBenchmarkSink, err = marshalOrderFrame(m)
		if err != nil {
			b.Fatal(err)
		}
	}
}
