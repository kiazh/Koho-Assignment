package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"koho-assignment/internal/api"
	"koho-assignment/internal/ledger"
)

func main() {
	path := os.Getenv("LEDGER_DB")
	if path == "" {
		path = "ledger.db"
	}
	address := os.Getenv("ADDR")
	if address == "" {
		address = "127.0.0.1:8080"
	}
	store, err := ledger.Open(path)
	if err != nil {
		slog.Error("open ledger", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		slog.Error("listen", "error", err)
		os.Exit(1)
	}
	slog.Info("ledger server listening", "address", address, "database", path)
	if err := serve(ctx, listener, api.New(store)); err != nil {
		slog.Error("serve", "error", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	shutdownDone := make(chan error, 1)
	stopShutdown := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			shutdownDone <- server.Shutdown(shutdownCtx)
		case <-stopShutdown:
		}
	}()
	err := server.Serve(listener)
	if !errors.Is(err, http.ErrServerClosed) {
		close(stopShutdown)
		return err
	}
	return <-shutdownDone
}
