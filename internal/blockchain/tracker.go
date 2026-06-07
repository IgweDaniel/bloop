package blockchain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/igwedaniel/bloop/internal/blockchain/base"
	"github.com/igwedaniel/bloop/internal/blockchain/bitcoin"
	"github.com/igwedaniel/bloop/internal/blockchain/ethereum"
	"github.com/igwedaniel/bloop/internal/blockchain/solana"
	"github.com/igwedaniel/bloop/internal/blockchain/tron"
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

type TrackerManager struct {
	trackers    map[types.BlockchainType]Tracker
	evmConfigs  map[types.BlockchainType]*config.EVMConfig
	utxoConfigs map[types.BlockchainType]*config.UTXOConfig
	cfg         *config.Config
	storage     storage.Storage
	publisher   messaging.Publisher
	logger      *logrus.Logger
}

func NewTrackerManager(cfg *config.Config, storage storage.Storage, publisher messaging.Publisher, logger *logrus.Logger) *TrackerManager {
	evmConfigs := make(map[types.BlockchainType]*config.EVMConfig, len(cfg.EVM))
	for i := range cfg.EVM {
		evmConfigs[cfg.EVM[i].Network] = &cfg.EVM[i]
	}
	utxoConfigs := make(map[types.BlockchainType]*config.UTXOConfig, len(cfg.UTXO))
	for i := range cfg.UTXO {
		utxoConfigs[cfg.UTXO[i].Network] = &cfg.UTXO[i]
	}

	return &TrackerManager{
		trackers:    make(map[types.BlockchainType]Tracker),
		evmConfigs:  evmConfigs,
		utxoConfigs: utxoConfigs,
		cfg:         cfg,
		storage:     storage,
		publisher:   publisher,
		logger:      logger,
	}
}

func (tm *TrackerManager) StartTracker(ctx context.Context, network types.BlockchainType) error {
	if tracker, exists := tm.trackers[network]; exists {
		if tracker.IsRunning() {
			return fmt.Errorf("tracker for %s is already running", network)
		}
	}

	var tracker Tracker

	buildBaseCfg := func(confirmations, batchSize, maxConcurrentBlocks int, requeueDelay time.Duration) base.BaseTrackerConfig {
		return base.BaseTrackerConfig{
			Confirmations:       confirmations,
			BatchSize:           batchSize,
			MaxConcurrentBlocks: maxConcurrentBlocks,
			PollInterval:        15 * time.Second,
			CatchupBatchSize:    50,
			HealthCheckInterval: 30 * time.Second,
			RequeueDelay:        requeueDelay,
		}
	}

	if cfg, ok := tm.evmConfigs[network]; ok {
		proc, perr := ethereum.NewEthereumProcessor(cfg, tm.storage, tm.logger)
		if perr != nil {
			return fmt.Errorf("failed to create %s processor: %w", network, perr)
		}

		baseCfg := buildBaseCfg(cfg.Confirmations, cfg.BatchSize, cfg.MaxConcurrentBlocks, 5*time.Second)
		bt := base.NewBaseTracker(proc, tm.storage, tm.publisher, tm.logger, baseCfg)
		proc.SetBaseTracker(bt)
		tracker = bt

	} else if cfg, ok := tm.utxoConfigs[network]; ok {
		bproc, perr := bitcoin.NewBitcoinProcessor(cfg, tm.storage, tm.logger)
		if perr != nil {
			return fmt.Errorf("failed to create %s processor: %w", network, perr)
		}
		bcfg := buildBaseCfg(cfg.Confirmations, cfg.BatchSize, cfg.MaxConcurrentBlocks, cfg.RequeueDelay)
		bbt := base.NewBaseTracker(bproc, tm.storage, tm.publisher, tm.logger, bcfg)
		bproc.SetBaseTracker(bbt)
		tracker = bbt

	} else {
		switch network {
		case types.Tron:
			tproc, perr := tron.NewProcessor(&tm.cfg.Tron, tm.storage, tm.logger)
			if perr != nil {
				return fmt.Errorf("failed to create TRON processor: %w", perr)
			}
			tcfg := buildBaseCfg(tm.cfg.Tron.Confirmations, tm.cfg.Tron.BatchSize, tm.cfg.Tron.MaxConcurrentBlocks, tm.cfg.Tron.RequeueDelay)
			tbt := base.NewBaseTracker(tproc, tm.storage, tm.publisher, tm.logger, tcfg)
			tproc.SetBaseTracker(tbt)
			tracker = tbt

		case types.Solana:
			sproc, perr := solana.NewProcessor(&tm.cfg.Solana, tm.storage, tm.logger)
			if perr != nil {
				return fmt.Errorf("failed to create SOLANA processor: %w", perr)
			}
			scfg := buildBaseCfg(tm.cfg.Solana.Confirmations, tm.cfg.Solana.BatchSize, tm.cfg.Solana.MaxConcurrentBlocks, tm.cfg.Solana.RequeueDelay)
			sbt := base.NewBaseTracker(sproc, tm.storage, tm.publisher, tm.logger, scfg)
			sproc.SetBaseTracker(sbt)
			tracker = sbt

		default:
			return fmt.Errorf("unsupported blockchain network: %s", network)
		}
	}

	if err := tracker.Start(ctx); err != nil {
		return fmt.Errorf("failed to start tracker for %s: %w", network, err)
	}

	tm.trackers[network] = tracker
	return nil
}

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

func (tm *TrackerManager) GetTracker(network types.BlockchainType) (Tracker, bool) {
	tracker, exists := tm.trackers[network]
	return tracker, exists
}

func (tm *TrackerManager) GetAllTrackers() map[types.BlockchainType]Tracker {
	result := make(map[types.BlockchainType]Tracker)
	for network, tracker := range tm.trackers {
		result[network] = tracker
	}
	return result
}

func (tm *TrackerManager) GetStats() map[types.BlockchainType]types.TrackerStats {
	stats := make(map[types.BlockchainType]types.TrackerStats)
	for network, tracker := range tm.trackers {
		stats[network] = tracker.GetStats()
	}
	return stats
}

func (tm *TrackerManager) AddWatchedWallet(ctx context.Context, network types.BlockchainType, address, walletID string) error {
	if !tm.IsSupported(network) {
		return fmt.Errorf("unsupported blockchain network: %s", network)
	}

	if _, ok := tm.evmConfigs[network]; ok {
		address = strings.ToLower(address)
	} else if network == types.Tron {
		normalized, err := tron.NormalizeAddress(address)
		if err != nil {
			return fmt.Errorf("invalid TRON address: %w", err)
		}
		address = normalized
	}

	if tracker, exists := tm.trackers[network]; exists {
		return tracker.AddWatchedWallet(ctx, address, walletID)
	}

	return tm.storage.AddWatchedWallet(ctx, network, address, walletID)
}

func (tm *TrackerManager) RemoveWatchedWallet(ctx context.Context, network types.BlockchainType, address string) error {
	if !tm.IsSupported(network) {
		return fmt.Errorf("unsupported blockchain network: %s", network)
	}

	if _, ok := tm.evmConfigs[network]; ok {
		address = strings.ToLower(address)
	} else if network == types.Tron {
		normalized, err := tron.NormalizeAddress(address)
		if err != nil {
			return fmt.Errorf("invalid TRON address: %w", err)
		}
		address = normalized
	}

	if tracker, exists := tm.trackers[network]; exists {
		return tracker.RemoveWatchedWallet(ctx, address)
	}

	return tm.storage.RemoveWatchedWallet(ctx, network, address)
}

func (tm *TrackerManager) IsSupported(network types.BlockchainType) bool {
	if _, ok := tm.evmConfigs[network]; ok {
		return true
	}
	if _, ok := tm.utxoConfigs[network]; ok {
		return true
	}

	switch network {
	case types.Tron, types.Solana:
		return true
	default:
		return false
	}
}

func (tm *TrackerManager) GetSupportedNetworks() []types.BlockchainType {
	networks := []types.BlockchainType{types.Tron, types.Solana}
	for _, chain := range tm.cfg.EVM {
		networks = append(networks, chain.Network)
	}
	for _, chain := range tm.cfg.UTXO {
		networks = append(networks, chain.Network)
	}
	return networks
}
