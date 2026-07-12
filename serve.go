package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/callum/cryptomatord/internal/api"
	"github.com/callum/cryptomatord/internal/config"
	"github.com/callum/cryptomatord/internal/supervisor"
)

// shutdownTimeout bounds unmounting every vault on daemon stop.
const shutdownTimeout = 45 * time.Second

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", os.Getenv("CRYPTOMATORD_CONFIG"), "path to JSON config file")
	logLevel := fs.String("log-level", "info", "log level: debug|info|warn|error")
	_ = fs.Parse(args)

	logger := newLogger(*logLevel)

	if *cfgPath == "" {
		logger.Error("serve: --config is required (or set CRYPTOMATORD_CONFIG)")
		return 2
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.Error("load config", "error", err)
		return 1
	}

	mgr := supervisor.NewManager(cfg, logger)
	srv := api.NewServer(mgr, cfg.Socket, logger)
	if err := srv.Listen(); err != nil {
		logger.Error("cannot listen on control socket", "socket", cfg.Socket, "error", err)
		return 1
	}

	logger.Info("cryptomatord started", "socket", cfg.Socket, "vaults", len(cfg.Vaults))
	mgr.Start()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()

	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigC:
		logger.Info("received signal, shutting down", "signal", sig.String())
	case err := <-serveErr:
		if err != nil {
			logger.Error("control server failed", "error", err)
		}
	}

	// Stop accepting requests, then unmount everything cleanly.
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	srv.Shutdown(ctx)
	mgr.Shutdown(ctx)
	logger.Info("stopped")
	return 0
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
}
