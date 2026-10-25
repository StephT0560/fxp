// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"go_gateway/diagnostics"
	"go_gateway/logging"
	"go_gateway/startup"
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
	stopDiagnostics, diagnosticErr := diagnostics.Start()
	if diagnosticErr != nil {
		log.Fatalf("diagnostics: %v", diagnosticErr)
	}
	defer stopDiagnostics()
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	manager, err := tlsconfig.ServerFromEnv()
	if err != nil {
		log.Fatalf("TLS configuration: %v", err)
	}
	if err := startup.ValidateSecrets(startup.Address("FXP_GW_TCP_ADDR", "127.0.0.1:8082"), startup.Address("FXP_GW_WS_ADDR", "127.0.0.1:8081")); err != nil {
		log.Fatalf("security configuration: %v", err)
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

	failed := false
	log.Println("🚀 Go API Gateway is running...")
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if err != nil {
			failed = true
			log.Printf("gateway server stopped: %v", err)
		}
		cancel()
	}
	wg.Wait()
	transport.ShutdownPool()
	close(serverErrors)
	for err := range serverErrors {
		if err != nil {
			failed = true
			log.Printf("gateway server stopped: %v", err)
		}
	}
	if failed {
		os.Exit(1)
	}
}
