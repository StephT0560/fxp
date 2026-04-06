package transport

import (
	"encoding/binary"
	"io"
	"log"
	"net"
	"sync"

	"go_gateway/protobuf"

	"google.golang.org/protobuf/proto"
)

const rustAddress = "127.0.0.1:8080" // FXP Core Address

var (
	conn     net.Conn
	connLock sync.Mutex
)

// StartTCPServer initializes the TCP server and handles incoming client connections
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

// handleTCPClient processes incoming messages from TCP clients
func handleTCPClient(clientConn net.Conn) {
	defer clientConn.Close()

	for {
		// Read 4-byte length prefix
		lenBuf := make([]byte, 4)
		_, err := io.ReadFull(clientConn, lenBuf)
		if err != nil {
			log.Printf("❌ Failed to read message length: %v", err)
			return
		}
		msgLen := binary.BigEndian.Uint32(lenBuf)

		// Read full message
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
		}

		HandleFXPMessage(clientConn, msg)
	}
}


// EnsureConnection maintains a persistent TCP connection to FXP Core
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

// SendOrderToRust forwards an order to Rust and returns execution report to the client
func SendOrderToRust(clientConn net.Conn, orderData []byte) {
	err := EnsureConnection()
	if err != nil {
		log.Printf("❌ Failed to connect to FXP Core: %v", err)
		return
	}

	msg := &protobuf.FXPMessage{}
	if err := proto.Unmarshal(orderData, msg); err == nil {
		if order, ok := msg.Payload.(*protobuf.FXPMessage_NewOrder); ok {
			orderID := order.NewOrder.OrderId
			orderClientMap.Store(orderID, clientConn)
			log.Printf("🧠 Registered OrderID %s to clientConn", orderID)
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
	var header = make([]byte, 4)

	for {
		// Step 1: Read the 4-byte length prefix
		_, err := io.ReadFull(conn, header)
		if err != nil {
			log.Printf("❌ Failed to read length prefix from FXP Core: %v", err)
			conn = nil
			return
		}

		// Step 2: Decode length prefix (big-endian uint32)
		msgLen := int(binary.BigEndian.Uint32(header))
		if msgLen <= 0 || msgLen > 4096 {
			log.Printf("⚠️ Invalid FXP message length: %d", msgLen)
			continue
		}

		// Step 3: Read message body
		body := make([]byte, msgLen)
		_, err = io.ReadFull(conn, body)
		if err != nil {
			log.Printf("❌ Failed to read FXP message body: %v", err)
			conn = nil
			return
		}

		log.Printf("📥 Market data update received from FXP Core (%d bytes): %x", msgLen, body)

		// Step 4: Unmarshal into FXPMessage
		msg := &protobuf.FXPMessage{}
		if err := proto.Unmarshal(body, msg); err != nil {
			log.Println("❌ Failed to decode FXPMessage from FXP Core:", err)
			continue
		}

		// Step 5: Route by payload type
		switch payload := msg.Payload.(type) {
		case *protobuf.FXPMessage_ExecutionReport:
			orderID := payload.ExecutionReport.OrderId
			log.Printf("📩 ExecutionReport received from FXP Core for OrderID %s", orderID)

			if clientAny, ok := orderClientMap.Load(orderID); ok {
				if client, ok := clientAny.(net.Conn); ok {
					// Re-encode with length prefix
					response, _ := proto.Marshal(msg)
					frame := make([]byte, 4+len(response))
					binary.BigEndian.PutUint32(frame[:4], uint32(len(response)))
					copy(frame[4:], response)

					_, err := client.Write(frame)
					if err != nil {
						log.Printf("❌ Failed to send ExecutionReport to TCP client: %v", err)
					} else {
						log.Printf("📩 ExecutionReport routed to TCP client for OrderID %s", orderID)
					}
					orderClientMap.Delete(orderID)
				}
			} else {
				log.Printf("⚠️ No registered client for OrderID %s — broadcast only", orderID)
			}

		default:
			log.Printf("🔍 FXP Payload Type Received: %T", msg.Payload)
			

			switch msg.Payload.(type) {
			case *protobuf.FXPMessage_MarketDataIncremental, *protobuf.FXPMessage_TradeCaptureReport:
				log.Println("📡 Routing market data update to HandleFXPMessage")
				HandleFXPMessage("market", msg)
			default:
				log.Println("⚠️ Unknown or unhandled FXPMessage payload from FXP Core")
			}
		}
	}
}



