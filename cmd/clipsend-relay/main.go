// clipsend-relay runs the private notification relay behind a TLS proxy.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"clipsend/internal/relay"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	config := flag.String("config", "/etc/clipsend-relay/config.json", "private service config path")
	flag.Parse()
	c, err := relay.LoadConfig(*config)
	if err != nil {
		return errors.New("clipsend-relay: invalid service configuration")
	}
	s, err := relay.NewService(c)
	if err != nil {
		return errors.New("clipsend-relay: service initialization failed")
	}
	addr := c.Listen
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("clipsend-relay: server failed to listen")
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = srv.Close()
			return errors.New("clipsend-relay: graceful shutdown failed")
		}
		<-done
		return nil
	}
}
