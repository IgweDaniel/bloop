package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/igwedaniel/bloop/internal/blockchain"
	"github.com/igwedaniel/bloop/internal/config"
	"github.com/igwedaniel/bloop/internal/storage"
	"github.com/sirupsen/logrus"
)

// Server represents the HTTP API server
type Server struct {
	server         *http.Server
	handlers       *Handlers
	trackerManager *blockchain.TrackerManager
	logger         *logrus.Logger
}

// NewServer creates a new API server
func NewServer(
	cfg *config.ServerConfig,
	trackerManager *blockchain.TrackerManager,
	storage storage.Storage,
	logger *logrus.Logger,
) *Server {
	handlers := NewHandlers(trackerManager, storage, logger)

	mux := http.NewServeMux()

	// Health check endpoint
	mux.HandleFunc("/health", handlers.HealthCheck)

	// Tracker endpoints
	mux.HandleFunc("/api/v1/trackers/stats", handlers.GetTrackerStats)
	mux.HandleFunc("/api/v1/networks", handlers.GetSupportedNetworks)

	// Wallet management endpoints
	mux.HandleFunc("/api/v1/wallets/watch", handlers.AddWatchedWallet)
	mux.HandleFunc("/api/v1/wallets/unwatch", handlers.RemoveWatchedWallet)
	mux.HandleFunc("/api/v1/wallets", handlers.GetWatchedWallets)

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      loggingMiddleware(mux, logger),
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	return &Server{
		server:         server,
		handlers:       handlers,
		trackerManager: trackerManager,
		logger:         logger,
	}
}

// Start starts the HTTP server
func (s *Server) Start() error {
	s.logger.Infof("Starting HTTP server on %s", s.server.Addr)

	if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("failed to start HTTP server: %w", err)
	}

	return nil
}

// Stop gracefully shuts down the HTTP server
func (s *Server) Stop(ctx context.Context) error {
	s.logger.Info("Shutting down HTTP server...")
	return s.server.Shutdown(ctx)
}

// loggingMiddleware logs HTTP requests
func loggingMiddleware(next http.Handler, logger *logrus.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create a response writer wrapper to capture status code
		wrapper := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(wrapper, r)

		duration := time.Since(start)

		logger.WithFields(logrus.Fields{
			"method":      r.Method,
			"path":        r.URL.Path,
			"status":      wrapper.statusCode,
			"duration":    duration,
			"remote_addr": r.RemoteAddr,
			"user_agent":  r.UserAgent(),
		}).Info("HTTP request")
	})
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
