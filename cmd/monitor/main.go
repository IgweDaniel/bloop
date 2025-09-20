package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/igwedaniel/bloop/internal/api"
	"github.com/igwedaniel/bloop/internal/blockchain"
	"github.com/igwedaniel/bloop/internal/config"
	"github.com/igwedaniel/bloop/internal/messaging"
	"github.com/igwedaniel/bloop/internal/storage"
	"github.com/igwedaniel/bloop/internal/types"
	"github.com/sirupsen/logrus"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// Setup logger
	logger := setupLogger(cfg.Logging)
	logger.Info("Starting Bloop Blockchain Monitor")

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize storage
	storage, err := storage.NewRedisStorage(&cfg.Redis, logger)
	if err != nil {
		logger.Fatalf("Failed to initialize storage: %v", err)
	}
	defer storage.Close()

	// Test storage connection
	if err := storage.Ping(ctx); err != nil {
		logger.Fatalf("Failed to connect to storage: %v", err)
	}
	logger.Info("Storage connection established")

	// Initialize messaging
	publisher, err := messaging.NewRabbitMQPublisher(cfg.RabbitMQ.URL, cfg.RabbitMQ.Exchange, logger)
	if err != nil {
		logger.Fatalf("Failed to initialize messaging: %v", err)
	}
	defer publisher.Close()
	logger.Info("Messaging system initialized")

	trackerManager := blockchain.NewTrackerManager(cfg, storage, publisher, logger)
	// Start Ethereum tracker
	if err := trackerManager.StartTracker(ctx, types.Ethereum); err != nil {
		logger.Fatalf("Failed to start Ethereum tracker: %v", err)
	}
	logger.Info("Ethereum tracker started")

	if cfg.Bitcoin.APIURL != "" {
		if err := trackerManager.StartTracker(ctx, types.Bitcoin); err != nil {
			logger.Errorf("Failed to start Bitcoin tracker: %v", err)
		} else {
			logger.Info("Bitcoin tracker started")
		}
	}

	// Start HTTP API server
	apiServer := api.NewServer(&cfg.Server, trackerManager, storage, logger)
	go func() {
		if err := apiServer.Start(); err != nil {
			logger.Errorf("HTTP server error: %v", err)
		}
	}()

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Wait for shutdown signal
	<-sigChan
	logger.Info("Shutdown signal received, starting graceful shutdown...")

	// Create shutdown context with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// Stop API server
	if err := apiServer.Stop(shutdownCtx); err != nil {
		logger.Errorf("Error stopping API server: %v", err)
	}

	// Stop all trackers
	if err := trackerManager.StopAll(); err != nil {
		logger.Errorf("Error stopping trackers: %v", err)
	}

	logger.Info("Bloop Blockchain Monitor stopped")
}

func setupLogger(cfg config.LoggingConfig) *logrus.Logger {
	logger := logrus.New()

	// Set log level
	level, err := logrus.ParseLevel(cfg.Level)
	if err != nil {
		level = logrus.InfoLevel
	}
	logger.SetLevel(level)

	// Set log format
	if cfg.Format == "json" {
		logger.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: time.RFC3339,
		})
	} else {
		logger.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: time.RFC3339,
		})
	}

	return logger
}
