// SPDX-License-Identifier: MIT
package main

import (
	"go_gateway/protobuf"
	"net"
	"testing"
	"time"
)

func fakePhase(t *testing.T, c config, delay time.Duration, reject bool) (phaseResult, error) {
	t.Helper()
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = server.Close() }()
		time.Sleep(delay)
		for i := 0; i < c.Orders; i++ {
			m, err := readFramed(server)
			if err != nil {
				return
			}
			o := m.GetNewOrder()
			if o == nil {
				return
			}
			et, status := protobuf.ExecutionType_EXEC_NEW, protobuf.OrderStatus_ORDER_NEW
			if reject {
				et, status = protobuf.ExecutionType_EXEC_REJECTED, protobuf.OrderStatus_ORDER_REJECTED
			}
			report := &protobuf.ExecutionReport{OrderId: o.OrderId, ExecutionType: et, OrderStatus: status}
			if sendFramed(server, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{ExecutionReport: report}}) != nil {
				return
			}
			if reject {
				return
			}
			report.ExecutionType = protobuf.ExecutionType_EXEC_FILLED
			report.OrderStatus = protobuf.OrderStatus_ORDER_FILLED
			if sendFramed(server, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{ExecutionReport: report}}) != nil {
				return
			}
		}
	}()
	r, err := runClientPhase(client, c, "TEST", "phase", time.Now(), c.Orders)
	_ = client.Close()
	<-done
	return r, err
}

func TestOpenLoopIncludesBackloggedIntendedSendTimes(t *testing.T) {
	c := config{Mode: "open", Rate: 1000, Orders: 10, Timeout: time.Second, Quantity: 1, Price: 300000}
	r, err := fakePhase(t, c, 80*time.Millisecond, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Ack.TotalCount() != 10 || r.Fill.TotalCount() != 10 {
		t.Fatalf("lost samples: %d/%d", r.Ack.TotalCount(), r.Fill.TotalCount())
	}
	// Nine scheduled sends are already overdue when the blocked first write
	// completes. Timing at actual writes would incorrectly hide their ~70ms wait.
	if r.Ack.ValueAtQuantile(50) < int64(50*time.Millisecond) {
		t.Fatalf("coordinated omission: p50=%s", time.Duration(r.Ack.ValueAtQuantile(50)))
	}
	if r.MaxSendLag < int64(50*time.Millisecond) {
		t.Fatalf("sender backlog not reported: %d", r.MaxSendLag)
	}
}
func TestClosedLoopCompletesAndRejectsDoNotBecomeLatencySamples(t *testing.T) {
	c := config{Mode: "closed", Orders: 4, Timeout: time.Second, Quantity: 1, Price: 300000}
	r, err := fakePhase(t, c, 0, false)
	if err != nil || r.Ack.TotalCount() != 4 || r.Fill.TotalCount() != 4 {
		t.Fatalf("closed loop: %+v %v", r, err)
	}
	if _, err := fakePhase(t, c, 0, true); err == nil {
		t.Fatal("rejected order reported as success")
	}
}
func TestBenchmarkUsesEnvironmentSecretAndPreservesDevDefault(t *testing.T) {
	t.Setenv("FXP_JWT_SECRET", "custom-benchmark-key")
	if benchmarkSecret() != "custom-benchmark-key" {
		t.Fatal("hard-coded key")
	}
	t.Setenv("FXP_JWT_SECRET", "")
	if benchmarkSecret() != "dev-secret-change-in-production" {
		t.Fatal("dev default changed")
	}
}
func TestSummaryReportsRepetitionSpreadWithoutPoolingPercentiles(t *testing.T) {
	got := spread([]float64{5, 1, 3, 2, 100})
	if got.Median != 3 || got.Min != 1 || got.Max != 100 {
		t.Fatal(got)
	}
}

func TestMissingTerminalFillFailsInsteadOfPublishingOnlyFastAcks(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	go func() {
		for i := 0; i < 2; i++ {
			m, err := readFramed(server)
			if err != nil {
				return
			}
			_ = sendFramed(server, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{ExecutionReport: &protobuf.ExecutionReport{OrderId: m.GetNewOrder().OrderId, ExecutionType: protobuf.ExecutionType_EXEC_NEW}}})
		}
	}()
	c := config{Mode: "open", Orders: 2, Rate: 1000, Timeout: 30 * time.Millisecond, Quantity: 1}
	if _, err := runClientPhase(client, c, "TEST", "missing", time.Now(), 2); err == nil {
		t.Fatal("incomplete fill histogram treated as a complete run")
	}
}
func TestClosedLoopWaitsForCommonStartAndEachAcknowledgment(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	start := time.Now().Add(30 * time.Millisecond)
	observed := make(chan time.Time, 2)
	go func() {
		for i := 0; i < 2; i++ {
			m, err := readFramed(server)
			if err != nil {
				return
			}
			observed <- time.Now()
			time.Sleep(20 * time.Millisecond)
			for _, et := range []protobuf.ExecutionType{protobuf.ExecutionType_EXEC_NEW, protobuf.ExecutionType_EXEC_FILLED} {
				_ = sendFramed(server, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{ExecutionReport: &protobuf.ExecutionReport{OrderId: m.GetNewOrder().OrderId, ExecutionType: et, OrderStatus: protobuf.OrderStatus_ORDER_FILLED}}})
			}
		}
	}()
	c := config{Mode: "closed", Orders: 2, Timeout: time.Second, Quantity: 1}
	if _, err := runClientPhase(client, c, "TEST", "closed", start, 2); err != nil {
		t.Fatal(err)
	}
	first, second := <-observed, <-observed
	if first.Before(start) || second.Sub(first) < 20*time.Millisecond {
		t.Fatalf("closed-loop schedule violated: %s %s", first.Sub(start), second.Sub(first))
	}
}
