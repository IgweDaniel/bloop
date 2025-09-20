package blockchain

import (
	"context"
	"fmt"

	"time"

	"github.com/igwedaniel/bloop/internal/blockchain/base"
	"github.com/igwedaniel/bloop/internal/blockchain/ethereum"
	"github.com/igwedaniel/bloop/internal/config"
	"github.com/igwedaniel/bloop/internal/messaging"
	"github.com/igwedaniel/bloop/internal/storage"
	"github.com/igwedaniel/bloop/internal/types"
	"github.com/sirupsen/logrus"
)

// Tracker interface defines the contract for blockchain trackers
type Tracker interface {
	// Start begins monitoring the blockchain
	Start(ctx context.Context) error

	// Stop gracefully shuts down the tracker
	Stop() error

	// AddWatchedWallet adds a wallet to the watch list
	AddWatchedWallet(ctx context.Context, address, walletID string) error

	// RemoveWatchedWallet removes a wallet from the watch list
	RemoveWatchedWallet(ctx context.Context, address string) error

	// GetNetwork returns the blockchain network this tracker monitors
	GetNetwork() types.BlockchainType

	// IsRunning returns whether the tracker is currently running
	IsRunning() bool

	// GetStats returns performance statistics
	GetStats() types.TrackerStats
}

// TrackerManager manages multiple blockchain trackers
type TrackerManager struct {
	trackers  map[types.BlockchainType]Tracker
	cfg       *config.Config
	storage   storage.Storage
	publisher messaging.Publisher
	logger    *logrus.Logger
}

// NewTrackerManager creates a new tracker manager without a separate factory
func NewTrackerManager(cfg *config.Config, storage storage.Storage, publisher messaging.Publisher, logger *logrus.Logger) *TrackerManager {
	return &TrackerManager{
		trackers:  make(map[types.BlockchainType]Tracker),
		cfg:       cfg,
		storage:   storage,
		publisher: publisher,
		logger:    logger,
	}
}

// StartTracker starts monitoring for a specific blockchain network
func (tm *TrackerManager) StartTracker(ctx context.Context, network types.BlockchainType) error {
	if tracker, exists := tm.trackers[network]; exists {
		if tracker.IsRunning() {
			return fmt.Errorf("tracker for %s is already running", network)
		}
	}

	var tracker Tracker
	var err error
	switch network {
	case types.Ethereum:
		// Build Ethereum tracker inline (replace wrapper)
		proc, perr := ethereum.NewEthereumProcessor(&tm.cfg.Ethereum, tm.storage, tm.logger)
		if perr != nil {
			return fmt.Errorf("failed to create Ethereum processor: %w", perr)
		}

		baseCfg := base.BaseTrackerConfig{
			Confirmations:       tm.cfg.Ethereum.Confirmations,
			BatchSize:           tm.cfg.Ethereum.BatchSize,
			MaxConcurrentBlocks: tm.cfg.Ethereum.MaxConcurrentBlocks,
			PollInterval:        15 * time.Second,
			CatchupBatchSize:    50,
			HealthCheckInterval: 30 * time.Second,
		}

		bt := base.NewBaseTracker(proc, tm.storage, tm.publisher, tm.logger, baseCfg)
		proc.SetBaseTracker(bt)
		tracker = bt
	default:
		err = fmt.Errorf("unsupported blockchain network: %s", network)
	}
	if err != nil {
		return fmt.Errorf("failed to create tracker for %s: %w", network, err)
	}

	if err := tracker.Start(ctx); err != nil {
		return fmt.Errorf("failed to start tracker for %s: %w", network, err)
	}

	tm.trackers[network] = tracker
	return nil
}

// StopTracker stops monitoring for a specific blockchain network
func (tm *TrackerManager) StopTracker(network types.BlockchainType) error {
	tracker, exists := tm.trackers[network]
	if !exists {
		return fmt.Errorf("no tracker found for %s", network)
	}

	if err := tracker.Stop(); err != nil {
		return fmt.Errorf("failed to stop tracker for %s: %w", network, err)
	}

	delete(tm.trackers, network)
	return nil
}

// StopAll stops all running trackers
func (tm *TrackerManager) StopAll() error {
	var errors []error

	for network, tracker := range tm.trackers {
		if err := tracker.Stop(); err != nil {
			errors = append(errors, fmt.Errorf("failed to stop %s tracker: %w", network, err))
		}
	}

	tm.trackers = make(map[types.BlockchainType]Tracker)

	if len(errors) > 0 {
		return fmt.Errorf("errors stopping trackers: %v", errors)
	}

	return nil
}

// GetTracker returns the tracker for a specific network
func (tm *TrackerManager) GetTracker(network types.BlockchainType) (Tracker, bool) {
	tracker, exists := tm.trackers[network]
	return tracker, exists
}

// GetAllTrackers returns all active trackers
func (tm *TrackerManager) GetAllTrackers() map[types.BlockchainType]Tracker {
	result := make(map[types.BlockchainType]Tracker)
	for network, tracker := range tm.trackers {
		result[network] = tracker
	}
	return result
}

// GetStats returns statistics for all trackers
func (tm *TrackerManager) GetStats() map[types.BlockchainType]types.TrackerStats {
	stats := make(map[types.BlockchainType]types.TrackerStats)
	for network, tracker := range tm.trackers {
		stats[network] = tracker.GetStats()
	}
	return stats
}

// AddWatchedWallet adds a wallet to be monitored on a specific network
func (tm *TrackerManager) AddWatchedWallet(ctx context.Context, network types.BlockchainType, address, walletID string) error {
	tracker, exists := tm.trackers[network]
	if !exists {
		return fmt.Errorf("no tracker found for %s", network)
	}

	return tracker.AddWatchedWallet(ctx, address, walletID)
}

// RemoveWatchedWallet removes a wallet from monitoring on a specific network
func (tm *TrackerManager) RemoveWatchedWallet(ctx context.Context, network types.BlockchainType, address string) error {
	tracker, exists := tm.trackers[network]
	if !exists {
		return fmt.Errorf("no tracker found for %s", network)
	}

	return tracker.RemoveWatchedWallet(ctx, address)
}

// IsSupported checks if a blockchain network is supported
func (tm *TrackerManager) IsSupported(network types.BlockchainType) bool {
	switch network {
	case types.Ethereum:
		return true
	default:
		return false
	}
}

// GetSupportedNetworks returns all supported blockchain networks
func (tm *TrackerManager) GetSupportedNetworks() []types.BlockchainType {
	return []types.BlockchainType{types.Ethereum}
}
