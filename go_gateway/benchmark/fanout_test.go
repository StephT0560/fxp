// SPDX-License-Identifier: MIT
package main

import (
	"go_gateway/protobuf"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFanoutSubscribesAfterWarmupAndWaitsForSnapshot(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var warmup atomic.Int64
	var early atomic.Bool
	var snapshot atomic.Bool
	var measuredEarly atomic.Bool
	var clients sync.WaitGroup
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			clients.Add(1)
			go func() {
				defer clients.Done()
				defer func() { _ = conn.Close() }()
				for {
					m, err := readFramed(conn)
					if err != nil {
						return
					}
					switch {
					case m.GetLogon() != nil:
						_ = sendFramed(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_LogonAck{LogonAck: &protobuf.LogonAck{Accepted: true}}})
					case m.GetMarketDataRequest() != nil:
						if warmup.Load() != 2 {
							early.Store(true)
						}
						time.Sleep(5 * time.Millisecond)
						snapshot.Store(true)
						_ = sendFramed(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_MarketDataSnapshot{MarketDataSnapshot: &protobuf.MarketDataSnapshot{Symbol: "TEST"}}})
					case m.GetNewOrder() != nil:
						o := m.GetNewOrder()
						if strings.Contains(o.OrderId, "-warmup-") {
							warmup.Add(1)
						} else if !snapshot.Load() {
							measuredEarly.Store(true)
						}
						for _, kind := range []protobuf.ExecutionType{protobuf.ExecutionType_EXEC_NEW, protobuf.ExecutionType_EXEC_FILLED} {
							status := protobuf.OrderStatus_ORDER_NEW
							if kind == protobuf.ExecutionType_EXEC_FILLED {
								status = protobuf.OrderStatus_ORDER_FILLED
							}
							_ = sendFramed(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_ExecutionReport{ExecutionReport: &protobuf.ExecutionReport{OrderId: o.OrderId, ExecutionType: kind, OrderStatus: status}}})
						}
					}
				}
			}()
		}
	}()
	c := config{Mode: "closed", Address: listener.Addr().String(), Concurrency: 1, Subscribers: 1, Orders: 2, Warmup: 2, Timeout: time.Second, Symbol: "TEST", Symbols: "single", Quantity: 1}
	_, err = runRepetition(c, 1)
	_ = listener.Close()
	<-accepted
	clients.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if early.Load() || measuredEarly.Load() || !snapshot.Load() {
		t.Fatal("subscription did not bracket warmup and measurement")
	}
}
