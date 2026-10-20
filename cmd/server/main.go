package main

import (
	"context"
	"errors"
	"log/slog"
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

	server := &http.Server{
		Addr:              address,
		Handler:           api.New(store),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("shutdown", "error", err)
		}
	}()
	slog.Info("ledger server listening", "address", address, "database", path)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve", "error", err)
		os.Exit(1)
	}
}
