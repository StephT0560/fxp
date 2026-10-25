// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	cfg, err := configFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	flag.StringVar(&cfg.staticDir, "static-dir", cfg.staticDir, "directory containing built UI assets")
	flag.Parse()
	app, err := newApp(cfg)
	if err != nil {
		log.Fatal(err)
	}
	if cfg.allowPlaintext {
		log.Print("INSECURE LOCAL DEMO: plaintext listener explicitly enabled")
	}
	server := &http.Server{Addr: cfg.addr, Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		app.close()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Print("FXP demo API starting")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		app.close()
		log.Fatal("demo API listener failed")
	}
	app.close()
}
