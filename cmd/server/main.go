// Command server runs Discuss Hub with graceful shutdown and bounded HTTP work.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ernat-soltanbekov/discuss-hub/internal/auth"
	"github.com/ernat-soltanbekov/discuss-hub/internal/db"
	"github.com/ernat-soltanbekov/discuss-hub/internal/handlers"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func run() error {
	initOnly := flag.Bool("init-only", false, "create/validate the SQLite database and exit")
	demo := flag.Bool("demo", false, "seed clearly labelled sample content into an empty database")
	flag.Parse()
	secure, err := strconv.ParseBool(env("COOKIE_SECURE", "false"))
	if err != nil {
		return fmt.Errorf("COOKIE_SECURE must be true or false: %w", err)
	}
	store, err := db.Open(env("DB_PATH", "forum.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	if *demo {
		if err = store.SeedDemo(context.Background()); err != nil {
			return err
		}
	}
	if *initOnly {
		slog.Info("database ready")
		return nil
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	app, err := handlers.New(store, handlers.Config{SecureCookies: secure, Logger: logger})
	if err != nil {
		return err
	}
	server := &http.Server{Addr: env("ADDR", ":8080"), Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	finished := make(chan error, 1)
	go func() {
		logger.Info("listening", "address", server.Addr, "secure_cookies", secure)
		finished <- server.ListenAndServe()
	}()
	// Expired sessions are rejected on every lookup; pruning only reclaims disk.
	service, err := auth.New(store, bcrypt.DefaultCost)
	if err != nil {
		_ = server.Close()
		return err
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case err := <-finished:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ticker.C:
			pruneCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := service.Prune(pruneCtx)
			cancel()
			if err != nil {
				logger.Error("prune sessions", "error", err)
			}
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				_ = server.Close()
				return err
			}
			logger.Info("shutdown complete")
			return nil
		}
	}
}
