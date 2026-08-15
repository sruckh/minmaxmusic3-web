// Command server is the minmaxmusic3-web application server: a Go +
// html/template + htmx front-end for MiniMax Music 3 generation.
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

	"github.com/sruckh/minmaxmusic3-web/internal/config"
	"github.com/sruckh/minmaxmusic3-web/internal/server"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return errors.Join(errors.New("loading config"), err)
	}
	logger.Info("config loaded", "summary", cfg.Summary())

	srv, err := server.New(cfg, logger)
	if err != nil {
		return errors.Join(errors.New("building server"), err)
	}
	if err := srv.Start(); err != nil { // store open, migrations, worker boot
		return errors.Join(errors.New("starting services"), err)
	}
	defer srv.Close()

	// Fully-timed server: no slowloris exposure behind the proxy, and
	// in-flight requests survive deploys via graceful shutdown.
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Background worker: owns all RunPod traffic; drains on shutdown.
	go srv.RunWorkers(ctx)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Addr)
		if err := httpSrv.ListenAndServe(); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return errors.Join(errors.New("serving"), err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return errors.Join(errors.New("graceful shutdown"), err)
	}
	// Workers drain inside srv.Close() after the listener stops.
	logger.Info("stopped cleanly")
	return nil
}
