package main

import (
	"go_gateway/transport"
	"log"
	"sync"
)

func main() {
	var wg sync.WaitGroup

	wg.Add(2)

	// Start WebSocket Server
	go func() {
		defer wg.Done()
		transport.StartWebSocketServer()
	}()

	// Start TCP Server
	go func() {
		defer wg.Done()
		transport.StartTCPServer()
	}()

	log.Println("🚀 Go API Gateway is running...")

	wg.Wait()
}
