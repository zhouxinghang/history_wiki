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

	"github.com/zhouxinghang/history_wiki/server/internal/config"
	"github.com/zhouxinghang/history_wiki/server/internal/httpapi"
	"github.com/zhouxinghang/history_wiki/server/internal/store"
)

func main() {
	configuration, err := config.Load()
	if err != nil {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: configuration.LogLevel}))

	database, err := store.OpenWithOptions(context.Background(), configuration.DatabaseURL, store.Options{
		MaxConns: configuration.DatabaseMaxConns, MinConns: configuration.DatabaseMinConns,
		MaxConnLifetime: configuration.DatabaseMaxLifetime, MaxConnIdleTime: configuration.DatabaseMaxIdleTime,
	})
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	server := &http.Server{
		Addr: configuration.Address,
		Handler: httpapi.New(database, config.LatestMigrationVersion, logger, httpapi.Config{
			PublicBaseURL:      configuration.PublicBaseURL,
			SessionCookieName:  configuration.SessionCookie,
			CSRFCookieName:     configuration.CSRFCookie,
			StaticDirectory:    configuration.StaticDirectory,
			TrustedProxyCount:  configuration.TrustedProxyCount,
			SessionIdleTimeout: configuration.SessionIdleTimeout,
			SessionMaxLifetime: configuration.SessionMaxLifetime,
			PasswordParams:     configuration.PasswordParams,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	shutdownSignal, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-shutdownSignal.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			logger.Error("shutdown HTTP server", "error", err)
		}
	}()

	logger.Info("start HTTP server", "address", configuration.Address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serve HTTP", "error", err)
		os.Exit(1)
	}
}
