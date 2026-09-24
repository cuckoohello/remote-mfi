package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cuckoohello/remote-mfi-for-xcertplay/internal/biz"
	"github.com/cuckoohello/remote-mfi-for-xcertplay/internal/chip"
	"github.com/cuckoohello/remote-mfi-for-xcertplay/internal/config"
	"github.com/cuckoohello/remote-mfi-for-xcertplay/internal/httpapi"
	"github.com/cuckoohello/remote-mfi-for-xcertplay/internal/transport"
	"github.com/cuckoohello/remote-mfi-for-xcertplay/internal/ver"
)

const shutdownTimeout = 10 * time.Second

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("remote-mfi-for-xcertplay %s (commit=%s built=%s)\n", ver.Version, ver.Commit, ver.BuildDate)
		return
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "remote-mfi-for-xcertplay: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger := newLogger(cfg)
	slog.SetDefault(logger)
	if cfg.BearerToken == "" {
		logger.Warn("authentication disabled; all business and diagnostic endpoints are unauthenticated")
	}

	ch341, err := transport.NewCh341(cfg.USBIDs, transport.I2CSpeed(cfg.I2CSpeedKHz))
	if err != nil {
		return fmt.Errorf("initialize CH341 transport: %w", err)
	}
	defer ch341.Close()

	driver, err := chip.NewDriver(ch341, cfg.I2CAddress)
	if err != nil {
		return fmt.Errorf("initialize MFi driver: %w", err)
	}
	service, err := biz.NewService(driver, ch341)
	if err != nil {
		return fmt.Errorf("initialize service: %w", err)
	}
	handler, err := httpapi.NewHandler(
		service,
		logger,
		cfg.BearerToken,
		cfg.Location,
		cfg.RecentCapacity,
	)
	if err != nil {
		return fmt.Errorf("initialize HTTP API: %w", err)
	}

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	appContext, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	go probeChip(appContext, logger, service)

	serverError := make(chan error, 1)
	go func() {
		logger.Info(
			"server_start",
			"address", cfg.HTTPAddr,
			"version", ver.Version,
			"commit", ver.Commit,
			"usb_ids", fmt.Sprint(cfg.USBIDs),
			"i2c_address", fmt.Sprintf("0x%02x", cfg.I2CAddress),
			"i2c_speed_khz", cfg.I2CSpeedKHz,
		)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverError <- err
		}
		close(serverError)
	}()

	select {
	case <-appContext.Done():
		logger.Info("shutdown_requested", "signal", appContext.Err())
	case err := <-serverError:
		if err != nil {
			return fmt.Errorf("HTTP server: %w", err)
		}
	}

	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("server_stopped")
	return nil
}

func probeChip(ctx context.Context, logger *slog.Logger, service *biz.Service) {
	probeContext, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	protocolMajor, err := service.Probe(probeContext)
	if err != nil {
		logger.Warn("startup_probe_failed", "error", err)
		return
	}
	logger.Info("startup_probe_succeeded", "protocol_major", protocolMajor)
}

func newLogger(cfg config.Config) *slog.Logger {
	options := &slog.HandlerOptions{Level: cfg.LogLevel}
	var handler slog.Handler
	if cfg.LogFormat == "text" {
		handler = slog.NewTextHandler(os.Stdout, options)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, options)
	}
	return slog.New(handler)
}
