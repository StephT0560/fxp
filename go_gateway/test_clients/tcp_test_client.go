
package main

import (
    "encoding/binary"
    "encoding/hex"
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

func main() {
    log.Println("🔌 Connecting to TCP Server...")
    conn, err := net.Dial("tcp", tcpAddress)
    if err != nil {
        log.Fatalf("❌ TCP connection failed: %v", err)
    }
    defer conn.Close()
    log.Println("✅ Connected to TCP Server!")

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

    subData, err := proto.Marshal(subscription)
    if err != nil {
        log.Fatalf("❌ Failed to encode market data request: %v", err)
    }

    framed := make([]byte, 4+len(subData))
    binary.BigEndian.PutUint32(framed[:4], uint32(len(subData)))
    copy(framed[4:], subData)

    log.Printf("📤 Sending MarketDataRequest (%d bytes): %s", len(subData), hex.EncodeToString(subData))
    _, err = conn.Write(framed)
    if err != nil {
        log.Fatalf("❌ Failed to send market data subscription: %v", err)
    }
    log.Println("📡 Subscribed to Market Data for ETH/USD!")

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
            default:
                log.Println("⚠️ Unknown FXPMessage received")
            }
        }
    }()

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
                    Sender:      "TCPTrader",
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

        orderData, err := proto.Marshal(order)
        if err != nil {
            log.Printf("❌ Failed to encode order: %v", err)
            continue
        }

        framedOrder := make([]byte, 4+len(orderData))
        binary.BigEndian.PutUint32(framedOrder[:4], uint32(len(orderData)))
        copy(framedOrder[4:], orderData)

        log.Printf("📤 Sending Order: ID=%s | Side=%s | Qty=%d | Price=%d",
            orderID, side.String(), quantity, price)

        _, err = conn.Write(framedOrder)
        if err != nil {
            log.Printf("❌ Failed to send order: %v", err)
            break
        }

        time.Sleep(3 * time.Second)
    }
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
