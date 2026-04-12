package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"strconv"
	"time"

	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const tcpAddress = "127.0.0.1:8082"

// ── Wire helpers ──────────────────────────────────────────────────────────────

func sendFramed(conn net.Conn, msg *protobuf.FXPMessage) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(data)))
	copy(frame[4:], data)
	_, err = conn.Write(frame)
	return err
}

func readFramedMessage(conn net.Conn) ([]byte, error) {
	lengthBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lengthBuf); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(lengthBuf)
	messageBuf := make([]byte, length)
	if _, err := io.ReadFull(conn, messageBuf); err != nil {
		return nil, err
	}
	return messageBuf, nil
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	// Secret must match FXP_JWT_SECRET on the server.
	// For dev, hardcode here. In production, read from env or a secrets store.
	jwtSecret := "dev-secret-change-in-production"
	traderID := "TCPTrader"
	sessionID := "tcp-session-" + strconv.Itoa(rand.Intn(100000))

	log.Println("🔌 Connecting to TCP Server...")
	conn, err := net.Dial("tcp", tcpAddress)
	if err != nil {
		log.Fatalf("❌ TCP connection failed: %v", err)
	}
	defer conn.Close()
	log.Println("✅ Connected to TCP Server!")

	// ── Step 1: Logon ─────────────────────────────────────────────────────────
	token := generateDevJWT(jwtSecret, traderID)
	log.Printf("🔑 Generated JWT for %s (session: %s)", traderID, sessionID)

	logon := &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_Logon{
			Logon: &protobuf.Logon{
				Token:          token,
				SessionId:      sessionID,
				EncryptionType: protobuf.EncryptionType_TLS_1_3,
				Timestamp:      timestamppb.Now(),
			},
		},
	}
	if err := sendFramed(conn, logon); err != nil {
		log.Fatalf("❌ Failed to send Logon: %v", err)
	}
	log.Println("📤 Logon sent — waiting for LogonAck...")

	// Read LogonAck
	ackBytes, err := readFramedMessage(conn)
	if err != nil {
		log.Fatalf("❌ Failed to read LogonAck: %v", err)
	}
	var ackMsg protobuf.FXPMessage
	if err := proto.Unmarshal(ackBytes, &ackMsg); err != nil {
		log.Fatalf("❌ Failed to decode LogonAck: %v", err)
	}
	ack, ok := ackMsg.Payload.(*protobuf.FXPMessage_LogonAck)
	if !ok {
		log.Fatalf("❌ Expected LogonAck, got: %T", ackMsg.Payload)
	}
	if !ack.LogonAck.Accepted {
		log.Fatalf("❌ Logon rejected: %s", ack.LogonAck.RejectReason)
	}
	log.Println("✅ Logon accepted — session established!")

	// ── Step 2: Subscribe to market data ──────────────────────────────────────
	subscription := &protobuf.FXPMessage{
		Payload: &protobuf.FXPMessage_MarketDataRequest{
			MarketDataRequest: &protobuf.MarketDataRequest{
				Header: &protobuf.FXPHeader{
					ProtocolVersion: "1.0",
					SequenceNumber:  1,
				},
				Symbols: []string{"ETH/USD"},
			},
		},
	}
	subData, _ := proto.Marshal(subscription)
	log.Printf("📤 Sending MarketDataRequest (%d bytes): %s", len(subData), hex.EncodeToString(subData))
	if err := sendFramed(conn, subscription); err != nil {
		log.Fatalf("❌ Failed to send MarketDataRequest: %v", err)
	}
	log.Println("📡 Subscribed to Market Data for ETH/USD!")

	// ── Step 3: Listen for server messages ────────────────────────────────────
	go func() {
		for {
			msg, err := readFramedMessage(conn)
			if err != nil {
				log.Fatalf("❌ Failed to read from server: %v", err)
			}

			var fxp protobuf.FXPMessage
			if err := proto.Unmarshal(msg, &fxp); err != nil {
				log.Printf("❌ Failed to decode received message: %v", err)
				continue
			}

			switch payload := fxp.Payload.(type) {
			case *protobuf.FXPMessage_ExecutionReport:
				log.Printf("📩 Execution Report → OrderID: %s | Status: %s | Price: %d",
					payload.ExecutionReport.OrderId,
					payload.ExecutionReport.OrderStatus.String(),
					payload.ExecutionReport.ExecutionPrice,
				)
			case *protobuf.FXPMessage_MarketDataIncremental:
				log.Printf("📈 MarketDataIncrementalUpdate for %s:", payload.MarketDataIncremental.Symbol)
				for _, change := range payload.MarketDataIncremental.Changes {
					log.Printf("  • UpdateType=%d | Price=%d | Qty=%d",
						change.UpdateType, change.Price, change.Quantity)
				}
			case *protobuf.FXPMessage_TradeCaptureReport:
				log.Printf("💥 TradeCaptureReport: %s | Buy=%s | Sell=%s | Price=%d | Qty=%d",
					payload.TradeCaptureReport.TradeId,
					payload.TradeCaptureReport.BuyOrderId,
					payload.TradeCaptureReport.SellOrderId,
					payload.TradeCaptureReport.TradePrice,
					payload.TradeCaptureReport.TradeQuantity)
			case *protobuf.FXPMessage_ErrorMessage:
				log.Printf("⚠️ Server error: [%s] %s",
					payload.ErrorMessage.ErrorType.String(),
					payload.ErrorMessage.ErrorMessage)
			default:
				log.Printf("⚠️ Unknown FXPMessage received: %T", payload)
			}
		}
	}()

	// ── Step 4: Send random orders every 3 seconds ────────────────────────────
	for {
		orderID := "TCP" + strconv.Itoa(rand.Intn(100000))
		price := int64(3100000000 + rand.Intn(300000000))
		quantity := int32(1 + rand.Intn(10))
		side := protobuf.OrderSide_BUY
		if rand.Intn(2) == 0 {
			side = protobuf.OrderSide_SELL
		}

		order := &protobuf.FXPMessage{
			Payload: &protobuf.FXPMessage_NewOrder{
				NewOrder: &protobuf.NewOrderSingle{
					Header: &protobuf.FXPHeader{
						ProtocolVersion: "1.0",
						SequenceNumber:  uint64(time.Now().UnixNano()),
					},
					OrderId:     orderID,
					Sender:      traderID,
					Receiver:    "ExchangeX",
					Symbol:      "ETH/USD",
					Price:       price,
					Side:        side,
					Quantity:    quantity,
					OrderType:   protobuf.OrderType_LIMIT,
					TimeInForce: protobuf.TimeInForce_DAY,
					Timestamp:   timestamppb.Now(),
				},
			},
		}

		log.Printf("📤 Sending Order: ID=%s | Side=%s | Qty=%d | Price=%d",
			orderID, side.String(), quantity, price)

		if err := sendFramed(conn, order); err != nil {
			log.Printf("❌ Failed to send order: %v", err)
			break
		}

		time.Sleep(3 * time.Second)
	}
}