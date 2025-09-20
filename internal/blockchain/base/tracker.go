package base

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/igwedaniel/bloop/internal/messaging"
	"github.com/igwedaniel/bloop/internal/storage"
	"github.com/igwedaniel/bloop/internal/types"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"
)

// BlockProcessor defines the interface that specific blockchain implementations must provide
type BlockProcessor interface {
	// ProcessBlock processes a single block and returns true if fully processed
	ProcessBlock(ctx context.Context, blockNumber uint64) (bool, error)

	// GetCurrentBlockHeight returns the current block height from the network
	GetCurrentBlockHeight(ctx context.Context) (uint64, error)

	// GetNetwork returns the blockchain network type
	GetNetwork() types.BlockchainType

	// InitializeProviders sets up RPC connections and providers
	InitializeProviders(ctx context.Context) error

	// CleanupProviders closes connections and cleans up resources
	CleanupProviders() error

	// SubscribeToNewBlocks sets up real-time block notifications (WebSocket/polling)
	SubscribeToNewBlocks(ctx context.Context, blockCh chan<- uint64) error
}

// BaseTrackerConfig contains common configuration for all trackers
type BaseTrackerConfig struct {
	Confirmations       int           `json:"confirmations"`
	BatchSize           int           `json:"batch_size"`
	MaxConcurrentBlocks int           `json:"max_concurrent_blocks"`
	PollInterval        time.Duration `json:"poll_interval"`
	CatchupBatchSize    int           `json:"catchup_batch_size"`
	HealthCheckInterval time.Duration `json:"health_check_interval"`
}

// BaseTracker provides common functionality for all blockchain trackers
type BaseTracker struct {
	processor BlockProcessor
	storage   storage.Storage
	publisher messaging.Publisher
	logger    *logrus.Logger
	config    BaseTrackerConfig

	blockSemaphore *semaphore.Weighted
	rpcSemaphore   *semaphore.Weighted

	isRunning bool
	stopCh    chan struct{}
	wg        sync.WaitGroup
	mu        sync.RWMutex

	processedBlocks uint64
	processedTxs    uint64
	errorCount      uint64
	startTime       time.Time

	blockCh chan uint64
}

// NewBaseTracker creates a new base tracker
func NewBaseTracker(
	processor BlockProcessor,
	storage storage.Storage,
	publisher messaging.Publisher,
	logger *logrus.Logger,
	config BaseTrackerConfig,
) *BaseTracker {
	return &BaseTracker{
		processor:      processor,
		storage:        storage,
		publisher:      publisher,
		logger:         logger,
		config:         config,
		blockSemaphore: semaphore.NewWeighted(int64(config.MaxConcurrentBlocks)),
		rpcSemaphore:   semaphore.NewWeighted(50), // Default RPC concurrency limit
		stopCh:         make(chan struct{}),
		blockCh:        make(chan uint64, 100),
		startTime:      time.Now(),
	}
}

// Start begins monitoring the blockchain
func (bt *BaseTracker) Start(ctx context.Context) error {
	bt.mu.Lock()
	if bt.isRunning {
		bt.mu.Unlock()
		return fmt.Errorf("tracker for %s is already running", bt.processor.GetNetwork())
	}
	bt.isRunning = true
	bt.mu.Unlock()

	network := bt.processor.GetNetwork()
	bt.logger.Infof("Starting %s tracker...", network)

	// Initialize blockchain-specific providers
	if err := bt.processor.InitializeProviders(ctx); err != nil {
		bt.isRunning = false
		return fmt.Errorf("failed to initialize providers: %w", err)
	}

	bt.wg.Add(1)
	go bt.blockProcessorLoop(ctx)

	bt.wg.Add(1)
	go bt.blockSubscriptionLoop(ctx)

	bt.wg.Add(1)
	go bt.healthMonitorLoop(ctx)

	go bt.performInitialCatchup(ctx)

	bt.logger.Infof("%s tracker started successfully", network)
	return nil
}

// Stop gracefully shuts down the tracker
func (bt *BaseTracker) Stop() error {
	bt.mu.Lock()
	if !bt.isRunning {
		bt.mu.Unlock()
		return nil
	}
	bt.isRunning = false
	bt.mu.Unlock()

	network := bt.processor.GetNetwork()
	bt.logger.Infof("Stopping %s tracker...", network)

	close(bt.stopCh)
	bt.wg.Wait()

	if err := bt.processor.CleanupProviders(); err != nil {
		bt.logger.Errorf("Error cleaning up providers: %v", err)
	}

	bt.logger.Infof("%s tracker stopped", network)
	return nil
}

func (bt *BaseTracker) IsRunning() bool {
	bt.mu.RLock()
	defer bt.mu.RUnlock()
	return bt.isRunning
}

func (bt *BaseTracker) GetStats() types.TrackerStats {
	bt.mu.RLock()
	defer bt.mu.RUnlock()

	watchedWallets, _ := bt.storage.GetWatchedWallets(context.Background(), bt.processor.GetNetwork())

	lastBlock, _ := bt.storage.GetLastProcessedBlock(context.Background(), bt.processor.GetNetwork())

	return types.TrackerStats{
		Network:         bt.processor.GetNetwork(),
		IsRunning:       bt.isRunning,
		ProcessedBlocks: bt.processedBlocks,
		ProcessedTxs:    bt.processedTxs,
		WatchedWallets:  len(watchedWallets),
		LastBlockHeight: lastBlock,
		Uptime:          time.Since(bt.startTime).String(),
		ErrorCount:      bt.errorCount,
	}
}

func (bt *BaseTracker) AddWatchedWallet(ctx context.Context, address, walletID string) error {
	return bt.storage.AddWatchedWallet(ctx, bt.processor.GetNetwork(), address, walletID)
}

func (bt *BaseTracker) RemoveWatchedWallet(ctx context.Context, address string) error {
	return bt.storage.RemoveWatchedWallet(ctx, bt.processor.GetNetwork(), address)
}

func (bt *BaseTracker) GetNetwork() types.BlockchainType {
	return bt.processor.GetNetwork()
}

func (bt *BaseTracker) blockProcessorLoop(ctx context.Context) {
	defer bt.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case <-bt.stopCh:
			return
		case blockNumber := <-bt.blockCh:
			bt.enqueueBlock(ctx, blockNumber, "realtime")
		}
	}
}

// blockSubscriptionLoop manages real-time block subscriptions
func (bt *BaseTracker) blockSubscriptionLoop(ctx context.Context) {
	defer bt.wg.Done()

	ticker := time.NewTicker(bt.config.PollInterval)
	defer ticker.Stop()

	// Try to establish real-time subscription
	subscriptionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		if err := bt.processor.SubscribeToNewBlocks(subscriptionCtx, bt.blockCh); err != nil {
			bt.logger.Warnf("Real-time subscription failed, falling back to polling: %v", err)
		}
	}()

	// Fallback polling
	for {
		select {
		case <-ctx.Done():
			return
		case <-bt.stopCh:
			return
		case <-ticker.C:
			bt.performPolling(ctx)
		}
	}
}

func (bt *BaseTracker) healthMonitorLoop(ctx context.Context) {
	defer bt.wg.Done()

	ticker := time.NewTicker(bt.config.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-bt.stopCh:
			return
		case <-ticker.C:
			bt.reportHealth()
		}
	}
}

// performInitialCatchup processes missed blocks since last run
func (bt *BaseTracker) performInitialCatchup(ctx context.Context) {
	network := bt.processor.GetNetwork()

	currentBlock, err := bt.processor.GetCurrentBlockHeight(ctx)
	if err != nil {
		bt.logger.Errorf("Failed to get current block number for %s catchup: %v", network, err)
		return
	}

	lastProcessed, err := bt.storage.GetLastProcessedBlock(ctx, network)
	if err != nil {
		bt.logger.Errorf("Failed to get last processed block for %s catchup: %v", network, err)
		return
	}

	if lastProcessed == 0 {
		// First run: set HWM to the current block and persist it
		lastProcessed = currentBlock
		if err := bt.storage.SetLastProcessedBlock(ctx, network, lastProcessed); err != nil {
			bt.logger.Errorf("Failed to persist initial last processed block for %s: %v", network, err)
			return
		}
		bt.logger.Infof("Initialized %s last processed to current block %d", network, lastProcessed)
	}

	// Check if we're already ahead or very close to current block
	if lastProcessed >= currentBlock {
		bt.logger.Infof("%s tracker is ahead of current block - last processed: %d, current: %d (will wait for new blocks)",
			network, lastProcessed, currentBlock)
		return
	}

	// If we're only a few blocks behind, no need for catchup - real-time will handle it
	if currentBlock-lastProcessed <= 10 {
		bt.logger.Infof("%s tracker is nearly up to date - last processed: %d, current: %d (only %d blocks behind, real-time will catch up)",
			network, lastProcessed, currentBlock, currentBlock-lastProcessed)
		return
	}

	blocksToProcess := currentBlock - lastProcessed
	if blocksToProcess > 0 {
		bt.logger.Infof("Performing initial %s catchup for %d blocks (from %d to %d)",
			network, blocksToProcess, lastProcessed+1, currentBlock)

		// Process in batches to avoid overwhelming the system
		batchSize := uint64(bt.config.CatchupBatchSize)
		for start := lastProcessed + 1; start <= currentBlock; start += batchSize {
			end := start + batchSize - 1
			if end > currentBlock {
				end = currentBlock
			}

			for blockNum := start; blockNum <= end; blockNum++ {
				if blockNum > currentBlock-uint64(bt.config.Confirmations) {
					break
				}
				bt.enqueueBlock(ctx, blockNum, "catchup")
			}

			// Small delay between batches
			select {
			case <-ctx.Done():
				return
			case <-bt.stopCh:
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
}

// performPolling checks for new blocks via polling
func (bt *BaseTracker) performPolling(ctx context.Context) {
	currentBlock, err := bt.processor.GetCurrentBlockHeight(ctx)
	if err != nil {
		bt.logger.Errorf("Failed to get current block number: %v", err)
		bt.errorCount++
		return
	}

	lastProcessed, err := bt.storage.GetLastProcessedBlock(ctx, bt.processor.GetNetwork())
	if err != nil {
		bt.logger.Errorf("Failed to get last processed block: %v", err)
		bt.errorCount++
		return
	}

	// Process missing blocks
	for blockNum := lastProcessed + 1; blockNum <= currentBlock; blockNum++ {
		if blockNum > currentBlock-uint64(bt.config.Confirmations) {
			break // Wait for more confirmations
		}

		select {
		case bt.blockCh <- blockNum:
		case <-ctx.Done():
			return
		case <-bt.stopCh:
			return
		default:
			// Channel full, skip this block for now
			bt.logger.Debugf("Block channel full, skipping block %d", blockNum)
		}
	}
}

func (bt *BaseTracker) enqueueBlock(ctx context.Context, blockNumber uint64, source string) {
	network := bt.processor.GetNetwork()

	processed, err := bt.storage.IsBlockProcessed(ctx, network, blockNumber)
	if err != nil {
		bt.logger.Errorf("Failed to check if block %d is processed: %v", blockNumber, err)
		bt.errorCount++
		return
	}
	if processed {
		return
	}

	if err := bt.blockSemaphore.Acquire(ctx, 1); err != nil {
		bt.logger.Errorf("Failed to acquire block semaphore: %v", err)
		return
	}

	go func() {
		defer bt.blockSemaphore.Release(1)
		if err := bt.processBlockSafely(ctx, blockNumber, source); err != nil {
			bt.logger.Errorf("Failed to process %s block %d from %s: %v", network, blockNumber, source, err)
			bt.errorCount++
		}
	}()
}

// processBlockSafely processes a block with error handling and metrics
func (bt *BaseTracker) processBlockSafely(ctx context.Context, blockNumber uint64, source string) error {
	startTime := time.Now()
	network := bt.processor.GetNetwork()

	// Check confirmations
	currentBlock, err := bt.processor.GetCurrentBlockHeight(ctx)
	if err != nil {
		return fmt.Errorf("failed to get current block height: %w", err)
	}

	confirmations := currentBlock - blockNumber
	if confirmations < uint64(bt.config.Confirmations) {
		// Not enough confirmations, requeue for later
		time.AfterFunc(30*time.Second, func() {
			select {
			case bt.blockCh <- blockNumber:
			default:
				// Channel full, will be picked up by polling
			}
		})
		return nil
	}

	// Process the block using the specific implementation
	fullyProcessed, err := bt.processor.ProcessBlock(ctx, blockNumber)
	if err != nil {
		return fmt.Errorf("failed to process block: %w", err)
	}

	if fullyProcessed {
		// Mark block as processed
		if err := bt.storage.MarkBlockProcessed(ctx, network, blockNumber); err != nil {
			return fmt.Errorf("failed to mark block as processed: %w", err)
		}

		// Update last processed block
		if err := bt.storage.SetLastProcessedBlock(ctx, network, blockNumber); err != nil {
			bt.logger.Errorf("Failed to update last processed block: %v", err)
		}

		// Best-effort: drop cached block now that we're done (avoid cache growth)
		_ = bt.storage.DeleteCache(ctx, fmt.Sprintf("%s:block:%d", network, blockNumber))

		// Advance HWM only when next contiguous blocks are past confirmations
		// Compute current height once for this advancement step
		cur, err := bt.processor.GetCurrentBlockHeight(ctx)
		if err != nil {
			bt.logger.Errorf("Failed to get current block height for HWM advance: %v", err)
		} else {
			// Only consider advancing if HWM+1 is <= cur-confirmations
			next := blockNumber + 1
			safeMax := cur - uint64(bt.config.Confirmations)
			if next <= safeMax {
				// Walk forward from existing HWM using bitmap knowledge
				if err := bt.storage.AdvanceHighWaterMark(ctx, network); err != nil {
					bt.logger.Errorf("Failed to advance high-water mark: %v", err)
				}
			}
		}

		// Update metrics
		bt.processedBlocks++

		processingTime := time.Since(startTime)
		bt.logger.WithFields(logrus.Fields{
			"network":         network,
			"block_number":    blockNumber,
			"source":          source,
			"processing_time": processingTime,
			"confirmations":   confirmations,
		}).Debug("Block processed successfully")
	}

	return nil
}

// reportHealth logs performance metrics
func (bt *BaseTracker) reportHealth() {
	uptime := time.Since(bt.startTime)
	blocksPerSecond := float64(bt.processedBlocks) / uptime.Seconds()
	txsPerSecond := float64(bt.processedTxs) / uptime.Seconds()

	bt.logger.WithFields(logrus.Fields{
		"network":           bt.processor.GetNetwork(),
		"uptime":            uptime,
		"processed_blocks":  bt.processedBlocks,
		"processed_txs":     bt.processedTxs,
		"error_count":       bt.errorCount,
		"blocks_per_second": blocksPerSecond,
		"txs_per_second":    txsPerSecond,
	}).Info("Tracker health report")
}

// PublishDeposit publishes a deposit event
func (bt *BaseTracker) PublishDeposit(ctx context.Context, deposit *types.WalletDeposit) error {
	if err := bt.publisher.PublishDeposit(ctx, deposit); err != nil {
		return fmt.Errorf("failed to publish deposit: %w", err)
	}

	bt.logger.WithFields(logrus.Fields{
		"network":      deposit.Network,
		"tx_hash":      deposit.TxHash,
		"wallet_id":    deposit.WalletID,
		"amount":       deposit.Amount,
		"currency":     deposit.Currency,
		"block_number": deposit.BlockNumber,
	}).Info("Deposit detected and published")

	return nil
}

// IncrementTxCount increments the processed transaction counter
func (bt *BaseTracker) IncrementTxCount(count uint64) {
	bt.mu.Lock()
	bt.processedTxs += count
	bt.mu.Unlock()
}
