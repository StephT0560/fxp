package main

import (
	"encoding/hex"
	"flag"
	"log"
	"math/rand"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const wsAddress = "ws://localhost:8081/ws"

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	singleRun := flag.Bool("single", false, "Run once and exit")
	continuousRun := flag.Bool("continuous", false, "Keep sending randomized orders")
	flag.Parse()

	if !*singleRun && !*continuousRun {
		log.Fatalf("❌ Please specify either --single or --continuous mode.")
	}

	jwtSecret := "dev-secret-change-in-production"
	traderID := "WebTrader"
	sessionID := "ws-session-" + strconv.Itoa(rand.Intn(100000))

	log.Println("🌐 Connecting to WebSocket Server...")
	u, _ := url.Parse(wsAddress)
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatalf("❌ WebSocket connection failed: %v", err)
	}
	defer conn.Close()
	log.Println("✅ Connected to WebSocket Server!")

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

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
	logonData, _ := proto.Marshal(logon)
	if err := conn.WriteMessage(websocket.BinaryMessage, logonData); err != nil {
		log.Fatalf("❌ Failed to send Logon: %v", err)
	}
	log.Println("📤 Logon sent — waiting for LogonAck...")

	// Read LogonAck
	_, ackBytes, err := conn.ReadMessage()
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
				Symbols: []string{"BTC/USD"},
			},
		},
	}
	subData, _ := proto.Marshal(subscription)
	if err := conn.WriteMessage(websocket.BinaryMessage, subData); err != nil {
		log.Fatalf("❌ Failed to send MarketDataRequest: %v", err)
	}
	log.Println("📡 Subscribed to Market Data for BTC/USD!")

	// ── Step 3: Listen for server messages ────────────────────────────────────
	go func() {
		for {
			processWebSocketMessages(conn)
		}
	}()

	// ── Step 4: Send orders ───────────────────────────────────────────────────
	if *singleRun {
		sendRandomOrder(conn, traderID)
	} else if *continuousRun {
		for {
			select {
			case <-interrupt:
				log.Println("🚪 Exiting: Received termination signal.")
				return
			default:
				sendRandomOrder(conn, traderID)
				time.Sleep(3 * time.Second)
			}
		}
	}
}

func sendRandomOrder(conn *websocket.Conn, traderID string) {
	orderID := "WS" + strconv.Itoa(rand.Intn(100000))
	price := int64(44500000000 + rand.Intn(1000000000))
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
				Symbol:      "BTC/USD",
				Price:       price,
				Side:        side,
				Quantity:    quantity,
				OrderType:   protobuf.OrderType_LIMIT,
				TimeInForce: protobuf.TimeInForce_DAY,
				Timestamp:   timestamppb.Now(),
			},
		},
	}

	orderData, _ := proto.Marshal(order)
	log.Printf("📤 Sending Order: ID=%s | Side=%s | Qty=%d | Price=%d",
		orderID, side.String(), quantity, price)
	_ = conn.WriteMessage(websocket.BinaryMessage, orderData)
}

func processWebSocketMessages(conn *websocket.Conn) {
	_, message, err := conn.ReadMessage()
	if err != nil {
		log.Printf("❌ Failed to receive WebSocket message: %v", err)
		return
	}

	log.Printf("📥 Raw message received (%d bytes): %s", len(message), hex.EncodeToString(message))

	fxpMsg := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(message, fxpMsg); err != nil {
		log.Printf("❌ Failed to decode FXPMessage: %v", err)
		return
	}

	switch payload := fxpMsg.Payload.(type) {
	case *protobuf.FXPMessage_ExecutionReport:
		report := payload.ExecutionReport
		log.Printf("📩 Execution Report → OrderID: %s | Status: %s | Price: %d",
			report.OrderId, mapOrderStatus(report.OrderStatus), report.ExecutionPrice)
	case *protobuf.FXPMessage_MarketDataIncremental:
		update := payload.MarketDataIncremental
		log.Printf("📡 Market Data Update → Symbol: %s | Changes: %+v", update.Symbol, update.Changes)
	case *protobuf.FXPMessage_LogonAck:
		// Already handled in main — log it if it appears unexpectedly
		log.Printf("ℹ️ LogonAck received in message loop: accepted=%v", payload.LogonAck.Accepted)
	case *protobuf.FXPMessage_ErrorMessage:
		log.Printf("⚠️ Server error: [%s] %s",
			payload.ErrorMessage.ErrorType.String(),
			payload.ErrorMessage.ErrorMessage)
	default:
		log.Printf("⚠️ Unknown FXPMessage type: %T", payload)
	}
}

func mapOrderStatus(status protobuf.OrderStatus) string {
	switch status {
	case protobuf.OrderStatus_ORDER_FILLED:
		return "Order Filled"
	case protobuf.OrderStatus_ORDER_PARTIALLY_FILLED:
		return "Partially Filled"
	case protobuf.OrderStatus_ORDER_CANCELED:
		return "Canceled"
	case protobuf.OrderStatus_ORDER_REJECTED:
		return "Rejected"
	default:
		return "Unknown"
	}
}