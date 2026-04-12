package transport

import (
	"encoding/binary"
	"io"
	"log"
	"net"
	"sync"

	"go_gateway/protobuf"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

const rustAddress = "127.0.0.1:8080"

var (
	conn     net.Conn
	connLock sync.Mutex
)

func StartTCPServer() {
	listener, err := net.Listen("tcp", ":8082")
	if err != nil {
		log.Fatalf("❌ Failed to start TCP server: %v", err)
	}
	defer listener.Close()

	log.Println("✅ TCP Server started on 127.0.0.1:8082")

	for {
		clientConn, err := listener.Accept()
		if err != nil {
			log.Println("❌ Failed to accept TCP connection:", err)
			continue
		}
		go handleTCPClient(clientConn)
	}
}

func handleTCPClient(clientConn net.Conn) {
	defer clientConn.Close()

	// sessionID is empty until a valid Logon is received for this connection.
	sessionID := ""

	for {
		lenBuf := make([]byte, 4)
		_, err := io.ReadFull(clientConn, lenBuf)
		if err != nil {
			log.Printf("❌ Failed to read message length: %v", err)
			return
		}
		msgLen := binary.BigEndian.Uint32(lenBuf)

		msgBuf := make([]byte, msgLen)
		_, err = io.ReadFull(clientConn, msgBuf)
		if err != nil {
			log.Printf("❌ Failed to read full FXPMessage: %v", err)
			return
		}

		msg := &protobuf.FXPMessage{}
		if err := proto.Unmarshal(msgBuf, msg); err != nil {
			log.Println("❌ Invalid FXPMessage from TCP client:", err)
			continue
		}

		if msg.Payload == nil {
			log.Println("⚠️ FXPMessage has no payload")
			continue
		}

		// Auth-aware router: enforces Logon before any other message type.
		HandleFXPMessageAuthenticated(clientConn, msg, &sessionID)
	}
}

func EnsureConnection() error {
	connLock.Lock()
	defer connLock.Unlock()

	if conn == nil {
		var err error
		conn, err = net.Dial("tcp", rustAddress)
		if err != nil {
			return err
		}
		log.Println("✅ Connected to FXP Core!")
		go ListenForMarketData()
	}
	return nil
}

// SendOrderToRust forwards a framed order (4-byte prefix + body) to FXP Core
// and registers the client connection for ExecutionReport delivery.
// orderData must be the pre-framed bytes (prefix + protobuf body).
func SendOrderToRust(clientConn net.Conn, orderData []byte) {
	err := EnsureConnection()
	if err != nil {
		log.Printf("❌ Failed to connect to FXP Core: %v", err)
		return
	}

	// FIX: orderData includes the 4-byte length prefix — strip it before
	// unmarshalling so we can extract the order ID for the client map.
	if len(orderData) > 4 {
		body := orderData[4:]
		msg := &protobuf.FXPMessage{}
		if err := proto.Unmarshal(body, msg); err == nil {
			if order, ok := msg.Payload.(*protobuf.FXPMessage_NewOrder); ok {
				orderID := order.NewOrder.OrderId
				orderClientMap.Store(orderID, clientConn)
				log.Printf("🧠 Registered OrderID %s to TCP clientConn", orderID)
			}
		}
	}

	connLock.Lock()
	defer connLock.Unlock()

	_, err = conn.Write(orderData)
	if err != nil {
		log.Printf("❌ Failed to send order to FXP Core: %v", err)
		conn.Close()
		conn = nil
		return
	}
	log.Println("📤 Order successfully forwarded to FXP Core")
}

func ListenForMarketData() {
	header := make([]byte, 4)

	for {
		_, err := io.ReadFull(conn, header)
		if err != nil {
			log.Printf("❌ Failed to read length prefix from FXP Core: %v", err)
			conn = nil
			return
		}

		msgLen := int(binary.BigEndian.Uint32(header))
		if msgLen <= 0 || msgLen > 1_048_576 {
			log.Printf("⚠️ Invalid FXP message length: %d", msgLen)
			continue
		}

		body := make([]byte, msgLen)
		_, err = io.ReadFull(conn, body)
		if err != nil {
			log.Printf("❌ Failed to read FXP message body: %v", err)
			conn = nil
			return
		}

		msg := &protobuf.FXPMessage{}
		if err := proto.Unmarshal(body, msg); err != nil {
			log.Println("❌ Failed to decode FXPMessage from FXP Core:", err)
			continue
		}

		switch payload := msg.Payload.(type) {
		case *protobuf.FXPMessage_ExecutionReport:
			orderID := payload.ExecutionReport.OrderId
			log.Printf("📩 ExecutionReport for OrderID %s status=%s",
				orderID, payload.ExecutionReport.OrderStatus)

			if clientAny, ok := orderClientMap.Load(orderID); ok {
				response, _ := proto.Marshal(msg)

				var sendErr error
				switch client := clientAny.(type) {
				case net.Conn:
					frame := make([]byte, 4+len(response))
					binary.BigEndian.PutUint32(frame[:4], uint32(len(response)))
					copy(frame[4:], response)
					_, sendErr = client.Write(frame)
				case *websocket.Conn:
					sendErr = client.WriteMessage(websocket.BinaryMessage, response)
				default:
					log.Printf("⚠️ Unknown client type in orderClientMap: %T", clientAny)
				}

				if sendErr != nil {
					log.Printf("❌ Failed to deliver ExecutionReport for OrderID %s: %v", orderID, sendErr)
				} else {
					log.Printf("✅ ExecutionReport delivered to client for OrderID %s", orderID)
				}

				// Only remove the client mapping on terminal statuses.
				// Partial fills send multiple reports for the same order ID.
				status := protobuf.OrderStatus(payload.ExecutionReport.OrderStatus)
				terminal := status == protobuf.OrderStatus_ORDER_FILLED ||
					status == protobuf.OrderStatus_ORDER_CANCELED ||
					status == protobuf.OrderStatus_ORDER_REJECTED
				if terminal {
					orderClientMap.Delete(orderID)
					log.Printf("🗑️ OrderID %s removed from client map (terminal status)", orderID)
				}
			} else {
				log.Printf("⚠️ No registered client for OrderID %s", orderID)
			}

		case *protobuf.FXPMessage_MarketDataIncremental:
			// FIX: call ForwardMarketData directly instead of HandleFXPMessage("market", ...)
			// which passed an invalid connection type and silently dropped the update.
			log.Printf("📡 MarketDataIncrementalUpdate for symbol: %s", payload.MarketDataIncremental.Symbol)
			ForwardMarketData(payload.MarketDataIncremental)

		case *protobuf.FXPMessage_TradeCaptureReport:
			log.Printf("📡 TradeCaptureReport for symbol: %s", payload.TradeCaptureReport.Symbol)
			ForwardMarketData(payload.TradeCaptureReport)

		default:
			log.Printf("⚠️ Unhandled FXPMessage payload from FXP Core: %T", msg.Payload)
		}
	}
}