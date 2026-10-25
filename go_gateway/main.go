// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"go_gateway/logging"
	"go_gateway/tlsconfig"
	"go_gateway/transport"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func main() {
	logging.Setup()
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	manager, err := tlsconfig.ServerFromEnv()
	if err != nil {
		log.Fatalf("TLS configuration: %v", err)
	}
	go manager.Watch(ctx, time.Second)
	var wg sync.WaitGroup
	serverErrors := make(chan error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		serverErrors <- transport.RunWebSocketServerWithTLS(ctx, manager)
	}()
	go func() {
		defer wg.Done()
		serverErrors <- transport.RunTCPServerWithTLS(ctx, manager)
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
