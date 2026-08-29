package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rizrmd/scraper/internal/brand24"
	"github.com/rizrmd/scraper/internal/server"
)

func main() {
	defaultAddr := "0.0.0.0:" + env("PORT", "3000")
	addr := flag.String("addr", env("ADDR", defaultAddr), "HTTP listen address")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := brand24.Config{
		AppURL:  env("BRAND24_APP_URL", "https://app.brand24.com"),
		DataURL: env("BRAND24_DATA_URL", "https://api-data.brand24.com"),
		Email:   os.Getenv("BRAND24_EMAIL"), Password: os.Getenv("BRAND24_PASSWORD"),
		APIKey: os.Getenv("BRAND24_API_KEY"), AccountID: os.Getenv("BRAND24_ACCOUNT_ID"),
		Retries: 4, Timeout: 45 * time.Second,
	}
	client, err := brand24.New(cfg, logger)
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	api := server.New(client, os.Getenv("API_TOKEN"), logger)
	httpServer := &http.Server{Addr: *addr, Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		logger.Info("brand24 scraper listening", "addr", *addr, "brand24_configured", cfg.APIKey != "" || (cfg.Email != "" && cfg.Password != ""))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "error", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdown)
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
