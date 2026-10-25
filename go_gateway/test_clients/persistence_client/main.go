// SPDX-License-Identifier: MIT
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"go_gateway/coreauth"
	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
)

const symbol = "PERSIST/E2E"

func send(conn net.Conn, message *protobuf.FXPMessage) error {
	body, err := proto.Marshal(message)
	if err != nil {
		return err
	}
	frame := make([]byte, 4, len(body)+4)
	binary.BigEndian.PutUint32(frame, uint32(len(body)))
	frame = append(frame, body...)
	_, err = io.Copy(conn, bytes.NewReader(frame))
	return err
}

func receive(conn net.Conn) (*protobuf.FXPMessage, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(conn, prefix[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length == 0 || length > 1_048_576 {
		return nil, fmt.Errorf("invalid frame length %d", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	message := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(body, message); err != nil {
		return nil, err
	}
	return message, nil
}

func snapshot(conn net.Conn) (*protobuf.MarketDataSnapshot, error) {
	if err := send(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_MarketDataRequest{
		MarketDataRequest: &protobuf.MarketDataRequest{Symbols: []string{symbol}},
	}}); err != nil {
		return nil, err
	}
	for {
		message, err := receive(conn)
		if err != nil {
			return nil, err
		}
		if result := message.GetMarketDataSnapshot(); result != nil {
			return result, nil
		}
	}
}

func report(conn net.Conn) (*protobuf.ExecutionReport, error) {
	for {
		message, err := receive(conn)
		if err != nil {
			return nil, err
		}
		if result := message.GetExecutionReport(); result != nil {
			return result, nil
		}
	}
}

func run(mode string) error {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:8080", 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := coreauth.Authenticate(conn, os.Getenv("FXP_CORE_AUTH_TOKEN")); err != nil {
		return err
	}
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	switch mode {
	case "seed", "hold":
		err := send(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_NewOrder{NewOrder: &protobuf.NewOrderSingle{
			Symbol: symbol, Sender: "persistence-check", OrderId: "resting-gtc",
			Price: 12345, Quantity: 7, Side: protobuf.OrderSide_BUY,
			OrderType: protobuf.OrderType_LIMIT, TimeInForce: protobuf.TimeInForce_GTC,
		}}})
		if err != nil {
			return err
		}
		ack, err := report(conn)
		if err != nil {
			return err
		}
		if ack.ExecutionType != protobuf.ExecutionType_EXEC_NEW || ack.ExecutionId != "X1" {
			return fmt.Errorf("unexpected seed acknowledgement: %v", ack)
		}
		if mode == "hold" {
			fmt.Println("READY")
			if err := conn.SetDeadline(time.Time{}); err != nil {
				return err
			}
			var b [1]byte
			n, err := conn.Read(b[:])
			if n != 0 || err != io.EOF {
				return fmt.Errorf("expected core shutdown EOF, read n=%d err=%v", n, err)
			}
			fmt.Println("EOF")
		} else {
			fmt.Println("✅ Results: 1/1 passed (core persistence seed)")
		}
	case "verify":
		book, err := snapshot(conn)
		if err != nil {
			return err
		}
		if book.Symbol != symbol || len(book.BidLevels) != 1 || book.BidLevels[0].Price != 12345 || book.BidLevels[0].Size != 7 {
			return fmt.Errorf("GTC missing after core restart: %v", book)
		}
		if err := send(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_OrderCancel{OrderCancel: &protobuf.OrderCancelRequest{
			Symbol: symbol, Sender: "persistence-check", OrderId: "resting-gtc", ClientCancelId: "cancel-restored",
		}}}); err != nil {
			return err
		}
		ack, err := report(conn)
		if err != nil {
			return err
		}
		if ack.ExecutionType != protobuf.ExecutionType_EXEC_CANCELED || ack.ExecutionId != "X2" || ack.OrderId != "resting-gtc" {
			return fmt.Errorf("restored GTC cancellation/ExecID failed: %v", ack)
		}
		book, err = snapshot(conn)
		if err != nil {
			return err
		}
		if len(book.BidLevels) != 0 || len(book.AskLevels) != 0 {
			return fmt.Errorf("restored order still on book after cancel: %v", book)
		}
		fmt.Println("✅ Results: 3/3 passed (core persistence restart, cancel, next ExecID)")
	default:
		return fmt.Errorf("expected seed, hold, or verify, got %q", mode)
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: persistence_client seed|hold|verify")
	}
	if err := run(os.Args[1]); err != nil {
		log.Fatal(err)
	}
}
