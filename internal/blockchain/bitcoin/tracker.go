package bitcoin

import (
	"fmt"
	"time"

	"github.com/igwedaniel/bloop/internal/blockchain/base"
	"github.com/igwedaniel/bloop/internal/config"
	"github.com/igwedaniel/bloop/internal/messaging"
	"github.com/igwedaniel/bloop/internal/storage"
	"github.com/sirupsen/logrus"
)

// BitcoinTracker wraps the base tracker with Bitcoin-specific functionality
type BitcoinTracker struct {
	*base.BaseTracker
	processor *BitcoinProcessor
}

// NewBitcoinTracker creates a new Bitcoin tracker using composition
func NewBitcoinTracker(
	cfg *config.BitcoinConfig, // TODO: Add to config
	storage storage.Storage,
	publisher messaging.Publisher,
	logger *logrus.Logger,
) (*BitcoinTracker, error) {
	// Create the Bitcoin processor
	processor, err := NewBitcoinProcessor(cfg, storage, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create Bitcoin processor: %w", err)
	}

	// Create base tracker configuration (Bitcoin-specific settings)
	baseConfig := base.BaseTrackerConfig{
		Confirmations:       6,                               // Bitcoin needs more confirmations
		BatchSize:           20,                              // Smaller batches for Bitcoin
		MaxConcurrentBlocks: 3,                               // Lower concurrency for Bitcoin
		PollInterval:        time.Duration(60) * time.Second, // Slower polling for Bitcoin
		CatchupBatchSize:    25,
		HealthCheckInterval: time.Duration(60) * time.Second,
	}

	// Create the base tracker
	baseTracker := base.NewBaseTracker(
		processor,
		storage,
		publisher,
		logger,
		baseConfig,
	)

	// Set the base tracker reference in the processor
	processor.SetBaseTracker(baseTracker)

	return &BitcoinTracker{
		BaseTracker: baseTracker,
		processor:   processor,
	}, nil
}
