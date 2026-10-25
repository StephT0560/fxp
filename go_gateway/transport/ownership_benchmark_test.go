// SPDX-License-Identifier: MIT
package transport

import (
	"go_gateway/protobuf"
	"testing"
)

var ownerBenchmarkSink string

func BenchmarkNamespacedOrderID(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		ownerBenchmarkSink = namespacedOrderID("TRADER-12345678", "ORDER-12345678901234567890")
	}
}
func BenchmarkBindOwner(b *testing.B) {
	m := &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_NewOrder{NewOrder: &protobuf.NewOrderSingle{}}}
	b.ReportAllocs()
	for b.Loop() {
		m.GetNewOrder().OrderId = "ORDER-12345678901234567890"
		bindOwner(m, "TRADER-12345678")
	}
	ownerBenchmarkSink = m.GetNewOrder().OrderId
}
