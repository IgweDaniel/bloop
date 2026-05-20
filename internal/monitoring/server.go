package monitoring

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

// Server exposes Prometheus metrics.
type Server struct {
	server *http.Server
	logger *logrus.Logger
}

// NewServer creates a dedicated metrics server.
func NewServer(port int, logger *logrus.Logger) *Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	return &Server{
		server: &http.Server{
			Addr:              fmt.Sprintf(":%d", port),
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		logger: logger,
	}
}

// Start starts the metrics HTTP server.
func (s *Server) Start() error {
	s.logger.Infof("Starting metrics server on %s", s.server.Addr)
	if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("failed to start metrics server: %w", err)
	}
	return nil
}

// Stop gracefully shuts down metrics server.
func (s *Server) Stop(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}
