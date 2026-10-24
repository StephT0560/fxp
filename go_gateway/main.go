// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"go_gateway/logging"
	"go_gateway/transport"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

func main() {
	logging.Setup()
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	var wg sync.WaitGroup
	serverErrors := make(chan error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		serverErrors <- transport.RunWebSocketServer(ctx)
	}()
	go func() {
		defer wg.Done()
		serverErrors <- transport.RunTCPServer(ctx)
	}()

	log.Println("🚀 Go API Gateway is running...")
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if err != nil {
			log.Printf("gateway server stopped: %v", err)
		}
		cancel()
	}
	wg.Wait()
	transport.ShutdownPool()
	close(serverErrors)
	for err := range serverErrors {
		if err != nil {
			log.Printf("gateway server stopped: %v", err)
			os.Exit(1)
		}
	}
}
