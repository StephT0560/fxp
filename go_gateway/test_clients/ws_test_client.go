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

func main() {
	singleRun := flag.Bool("single", false, "Run once and exit")
	continuousRun := flag.Bool("continuous", false, "Keep listening and sending randomized orders")
	flag.Parse()

	if !*singleRun && !*continuousRun {
		log.Fatalf("❌ Please specify either --single or --continuous mode.")
	}

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

	// Step 1: Subscribe to Market Data
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
	_ = conn.WriteMessage(websocket.BinaryMessage, subData)
	log.Println("📡 Subscribed to Market Data for BTC/USD!")

	// Step 2: Listen in a separate goroutine
	go func() {
		for {
			processWebSocketMessages(conn)
		}
	}()

	// Step 3: Continuously send randomized orders
	if *singleRun {
		sendRandomOrder(conn)
	} else if *continuousRun {
		for {
			select {
			case <-interrupt:
				log.Println("🚪 Exiting: Received termination signal.")
				return
			default:
				sendRandomOrder(conn)
				time.Sleep(3 * time.Second)
			}
		}
	}
}

// ✅ Send one randomized order
func sendRandomOrder(conn *websocket.Conn) {
	orderID := "WS" + strconv.Itoa(rand.Intn(100000))
	price := int64(44500000000 + rand.Intn(1000000000)) // ~44500–45500
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
				Sender:      "WebTrader",
				Receiver:    "ExchangeX",
				Symbol:      "ETH/USD",
				Price:       100,
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

// ✅ Process server responses
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
			report.OrderId, mapOrderStatusWS(report.OrderStatus), report.ExecutionPrice)

	case *protobuf.FXPMessage_MarketDataIncremental:
		update := payload.MarketDataIncremental
		log.Printf("📡 Market Data Update → Symbol: %s | Changes: %+v", update.Symbol, update.Changes)

	default:
		log.Printf("❌ Received unknown WebSocket message type: %T", payload)
	}
}

func mapOrderStatusWS(status protobuf.OrderStatus) string {
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
