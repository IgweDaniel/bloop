package base

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/igwedaniel/bloop/internal/messaging"
	"github.com/igwedaniel/bloop/internal/monitoring"
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

// BlockJob is the unit of work consumed by tracker workers.
// Source is used for routing, logs, and metrics.
type BlockJob struct {
	Number uint64
	Source string
}

const (
	blockSourceRealtime = "realtime"
	blockSourcePolling  = "polling"
	blockSourceCatchup  = "catchup"
	blockSourceRequeue  = "requeue"
)

// BaseTrackerConfig contains common configuration for all trackers
type BaseTrackerConfig struct {
	Confirmations       int           `json:"confirmations"`
	BatchSize           int           `json:"batch_size"`
	MaxConcurrentBlocks int           `json:"max_concurrent_blocks"`
	PollInterval        time.Duration `json:"poll_interval"`
	CatchupBatchSize    int           `json:"catchup_batch_size"`
	HealthCheckInterval time.Duration `json:"health_check_interval"`
	RequeueDelay        time.Duration `json:"requeue_delay"`

	// Optional split-queue tuning. If these are not set, MaxConcurrentBlocks
	// is split into realtime and catchup workers automatically.
	RealtimeWorkers int `json:"realtime_workers"`
	CatchupWorkers  int `json:"catchup_workers"`

	// Optional queue capacities. If not set, sensible defaults are derived
	// from the worker counts and CatchupBatchSize.
	RealtimeQueueCap int `json:"realtime_queue_cap"`
	CatchupQueueCap  int `json:"catchup_queue_cap"`
}

// BaseTracker provides common functionality for all blockchain trackers
type BaseTracker struct {
	processor BlockProcessor
	storage   storage.Storage
	publisher messaging.Publisher
	logger    *logrus.Logger
	config    BaseTrackerConfig

	rpcSemaphore *semaphore.Weighted

	isRunning bool
	stopCh    chan struct{}
	wg        sync.WaitGroup
	mu        sync.RWMutex

	runCtx    context.Context
	runCancel context.CancelFunc

	processedBlocks uint64
	processedTxs    uint64
	errorCount      uint64
	startTime       time.Time

	lastEnqueued            uint64
	lastDequeued            uint64
	lastProcessing          uint64
	skippedChannelFull      uint64
	skippedAlreadyProcessed uint64
	blockGapConsecutive     uint64

	realtimeCh     chan BlockJob
	catchupCh      chan BlockJob
	inFlightBlocks map[uint64]struct{}
}

// NewBaseTracker creates a new base tracker
func NewBaseTracker(
	processor BlockProcessor,
	storage storage.Storage,
	publisher messaging.Publisher,
	logger *logrus.Logger,
	config BaseTrackerConfig,
) *BaseTracker {
	realtimeWorkers, catchupWorkers := config.workerSplit()
	realtimeQueueCap, catchupQueueCap := config.queueCaps(realtimeWorkers, catchupWorkers)

	return &BaseTracker{
		processor:      processor,
		storage:        storage,
		publisher:      publisher,
		logger:         logger,
		config:         config,
		rpcSemaphore:   semaphore.NewWeighted(20), // Reduced from 50 for lower memory usage
		stopCh:         make(chan struct{}),
		realtimeCh:     make(chan BlockJob, realtimeQueueCap),
		catchupCh:      make(chan BlockJob, catchupQueueCap),
		inFlightBlocks: make(map[uint64]struct{}),
		startTime:      time.Now(),
	}
}

func (c BaseTrackerConfig) workerSplit() (int, int) {
	realtimeWorkers := c.RealtimeWorkers
	catchupWorkers := c.CatchupWorkers

	if realtimeWorkers > 0 || catchupWorkers > 0 {
		if realtimeWorkers <= 0 {
			realtimeWorkers = 1
		}
		if catchupWorkers <= 0 {
			catchupWorkers = 1
		}
		return realtimeWorkers, catchupWorkers
	}

	total := c.MaxConcurrentBlocks
	if total <= 0 {
		total = 1
	}

	// Default split: protect some capacity for live/retry work while leaving
	// most workers for catchup. For MaxConcurrentBlocks=100, this gives 20/80.
	realtimeWorkers = total / 5
	if realtimeWorkers < 1 {
		realtimeWorkers = 1
	}
	catchupWorkers = total - realtimeWorkers
	if catchupWorkers < 1 {
		catchupWorkers = 1
	}

	return realtimeWorkers, catchupWorkers
}

func (c BaseTrackerConfig) queueCaps(realtimeWorkers, catchupWorkers int) (int, int) {
	realtimeCap := c.RealtimeQueueCap
	if realtimeCap <= 0 {
		realtimeCap = realtimeWorkers * 20
	}
	if realtimeCap < 200 {
		realtimeCap = 200
	}

	catchupCap := c.CatchupQueueCap
	if catchupCap <= 0 {
		catchupCap = catchupWorkers * 50
	}
	if c.CatchupBatchSize > catchupCap {
		catchupCap = c.CatchupBatchSize
	}
	if catchupCap < 1000 {
		catchupCap = 1000
	}

	return realtimeCap, catchupCap
}

// Start begins monitoring the blockchain
func (bt *BaseTracker) Start(ctx context.Context) error {
	bt.mu.Lock()
	if bt.isRunning {
		bt.mu.Unlock()
		return fmt.Errorf("tracker for %s is already running", bt.processor.GetNetwork())
	}

	realtimeWorkers, catchupWorkers := bt.config.workerSplit()
	realtimeQueueCap, catchupQueueCap := bt.config.queueCaps(realtimeWorkers, catchupWorkers)

	bt.stopCh = make(chan struct{})
	bt.realtimeCh = make(chan BlockJob, realtimeQueueCap)
	bt.catchupCh = make(chan BlockJob, catchupQueueCap)
	bt.inFlightBlocks = make(map[uint64]struct{})
	bt.isRunning = true
	bt.mu.Unlock()

	network := bt.processor.GetNetwork()
	bt.logger.Infof("Starting %s tracker...", network)

	// Initialize blockchain-specific providers
	bt.runCtx, bt.runCancel = context.WithCancel(ctx)
	if err := bt.processor.InitializeProviders(bt.runCtx); err != nil {
		bt.mu.Lock()
		bt.isRunning = false
		bt.mu.Unlock()
		return fmt.Errorf("failed to initialize providers: %w", err)
	}

	for i := 0; i < realtimeWorkers; i++ {
		bt.wg.Add(1)
		go bt.blockWorker(bt.runCtx, i+1, blockSourceRealtime, bt.realtimeCh)
	}

	for i := 0; i < catchupWorkers; i++ {
		bt.wg.Add(1)
		go bt.blockWorker(bt.runCtx, i+1, blockSourceCatchup, bt.catchupCh)
	}

	bt.wg.Add(1)
	go bt.blockSubscriptionLoop(bt.runCtx)

	bt.wg.Add(1)
	go bt.healthMonitorLoop(bt.runCtx)

	bt.wg.Add(1)
	go func() {
		defer bt.wg.Done()
		bt.performInitialCatchup(bt.runCtx)
	}()

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

	if bt.runCancel != nil {
		bt.runCancel()
	}
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

	currentBlock, err := bt.processor.GetCurrentBlockHeight(context.Background())
	if err != nil {
		bt.logger.Errorf("Failed to get current block number: %v", err)
		currentBlock = 0
	}

	var safeHead uint64
	if currentBlock > uint64(bt.config.Confirmations) {
		safeHead = currentBlock - uint64(bt.config.Confirmations)
	}
	var blockGap uint64
	if safeHead > lastBlock {
		blockGap = safeHead - lastBlock
	}

	var inFlightTxs uint64
	if p, ok := bt.processor.(interface{ InFlightTxs() uint64 }); ok {
		inFlightTxs = p.InFlightTxs()
	}
	var apiProviders map[string]uint64
	var apiProviderErrors map[string]uint64
	var apiProviderLast string
	if p, ok := bt.processor.(interface {
		ProviderStats() (map[string]uint64, map[string]uint64, string)
	}); ok {
		apiProviders, apiProviderErrors, apiProviderLast = p.ProviderStats()
	}

	return types.TrackerStats{
		Network:             bt.processor.GetNetwork(),
		IsRunning:           bt.isRunning,
		ProcessedBlocks:     atomic.LoadUint64(&bt.processedBlocks),
		ProcessedTxs:        atomic.LoadUint64(&bt.processedTxs),
		WatchedWallets:      len(watchedWallets),
		LastBlockHeight:     lastBlock,
		CurrentBlockHeight:  currentBlock,
		SafeHead:            safeHead,
		BlockGap:            blockGap,
		Confirmations:       bt.config.Confirmations,
		BlockQueueLen:       len(bt.realtimeCh) + len(bt.catchupCh),
		BlockQueueCap:       cap(bt.realtimeCh) + cap(bt.catchupCh),
		LastEnqueuedBlock:   atomic.LoadUint64(&bt.lastEnqueued),
		LastDequeuedBlock:   atomic.LoadUint64(&bt.lastDequeued),
		LastProcessingBlock: atomic.LoadUint64(&bt.lastProcessing),
		SkippedChannelFull:  atomic.LoadUint64(&bt.skippedChannelFull),
		SkippedProcessed:    atomic.LoadUint64(&bt.skippedAlreadyProcessed),
		BlockGapConsecutive: atomic.LoadUint64(&bt.blockGapConsecutive),
		InFlightTxs:         inFlightTxs,
		APIProviders:        apiProviders,
		APIProviderErrors:   apiProviderErrors,
		APIProviderLast:     apiProviderLast,
		Uptime:              time.Since(bt.startTime).String(),
		ErrorCount:          atomic.LoadUint64(&bt.errorCount),
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

func (bt *BaseTracker) blockWorker(ctx context.Context, workerID int, workerKind string, ch <-chan BlockJob) {
	defer bt.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case <-bt.stopCh:
			return
		case job := <-ch:
			atomic.StoreUint64(&bt.lastDequeued, job.Number)
			bt.processBlockJob(ctx, job, workerID, workerKind)
		}
	}
}

func (bt *BaseTracker) enqueueJob(ctx context.Context, job BlockJob) bool {
	if job.Source == "" {
		job.Source = "unknown"
	}

	ch := bt.realtimeCh
	queueName := "realtime"
	if job.Source == blockSourceCatchup {
		ch = bt.catchupCh
		queueName = "catchup"
	}

	select {
	case ch <- job:
		atomic.StoreUint64(&bt.lastEnqueued, job.Number)
		return true
	case <-ctx.Done():
		return false
	case <-bt.stopCh:
		return false
	case <-time.After(2 * time.Second):
		bt.logger.WithFields(logrus.Fields{
			"network":      bt.processor.GetNetwork(),
			"block_number": job.Number,
			"source":       job.Source,
			"queue":        queueName,
			"queue_len":    len(ch),
			"queue_cap":    cap(ch),
		}).Warn("Block queue still full, deferring block")
		atomic.AddUint64(&bt.skippedChannelFull, 1)
		return false
	}
}

// blockSubscriptionLoop manages real-time block subscriptions
func (bt *BaseTracker) blockSubscriptionLoop(ctx context.Context) {
	defer bt.wg.Done()

	ticker := time.NewTicker(bt.config.PollInterval)
	defer ticker.Stop()

	// Keep the processor interface simple: blockchain-specific code still emits raw block numbers,
	// while the base tracker wraps them into BlockJob values before they enter the central queue.
	realtimeBlocks := make(chan uint64, cap(bt.realtimeCh))

	subscriptionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		if err := bt.processor.SubscribeToNewBlocks(subscriptionCtx, realtimeBlocks); err != nil {
			bt.logger.Warnf("Real-time subscription failed, falling back to polling: %v", err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			cancel()
			return
		case <-bt.stopCh:
			cancel()
			return
		case blockNumber, ok := <-realtimeBlocks:
			if !ok {
				bt.logger.Warn("Real-time block channel closed; polling will continue")
				realtimeBlocks = nil
				continue
			}
			bt.enqueueJob(ctx, BlockJob{Number: blockNumber, Source: blockSourceRealtime})
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

// normalizeLastProcessed clamps lastProcessed when it is ahead of currentBlock.
// This can happen if storage was seeded from a different network or a corrupted value.
func (bt *BaseTracker) normalizeLastProcessed(ctx context.Context, currentBlock uint64) uint64 {
	network := bt.processor.GetNetwork()
	lastProcessed, err := bt.storage.GetLastProcessedBlock(ctx, network)
	if err != nil {
		bt.logger.Errorf("Failed to get last processed block: %v", err)
		return 0
	}

	var safeHead uint64
	if currentBlock > uint64(bt.config.Confirmations) {
		safeHead = currentBlock - uint64(bt.config.Confirmations)
	}

	// Invariant: lastProcessed should never be ahead of safe head.
	// If this happens (e.g. out-of-order writes/manual drift), clamp it so polling can recover gaps.
	if safeHead > 0 && lastProcessed > safeHead {
		resetTo := safeHead
		bt.logger.WithFields(logrus.Fields{
			"network":        network,
			"last_processed": lastProcessed,
			"current_block":  currentBlock,
			"safe_head":      safeHead,
			"reset_to":       resetTo,
		}).Warn("Last processed exceeds safe head; resetting")

		if err := bt.storage.ForceSetLastProcessedBlock(ctx, network, resetTo); err != nil {
			bt.logger.Errorf("Failed to reset last processed block: %v", err)
			return lastProcessed
		}
		return resetTo
	}

	// Fallback safety for obviously invalid values.
	if lastProcessed > currentBlock {
		resetTo := currentBlock
		bt.logger.WithFields(logrus.Fields{
			"network":        network,
			"last_processed": lastProcessed,
			"current_block":  currentBlock,
			"reset_to":       resetTo,
		}).Warn("Last processed is ahead of current; resetting")

		if err := bt.storage.ForceSetLastProcessedBlock(ctx, network, resetTo); err != nil {
			bt.logger.Errorf("Failed to reset last processed block: %v", err)
			return lastProcessed
		}
		return resetTo
	}

	return lastProcessed
}

// performInitialCatchup processes missed blocks since last run
func (bt *BaseTracker) performInitialCatchup(ctx context.Context) {
	network := bt.processor.GetNetwork()

	currentBlock, err := bt.processor.GetCurrentBlockHeight(ctx)
	if err != nil {
		bt.logger.Errorf("Failed to get current block number for %s catchup: %v", network, err)
		return
	}

	lastProcessed := bt.normalizeLastProcessed(ctx, currentBlock)

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
		bt.logger.Infof("%s tracker is already up to date - last processed: %d, current: %d (will wait for new blocks)",
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
		if batchSize == 0 {
			batchSize = 1
		}

		safeHead := currentBlock
		if currentBlock > uint64(bt.config.Confirmations) {
			safeHead = currentBlock - uint64(bt.config.Confirmations)
		}

		for start := lastProcessed + 1; start <= currentBlock; start += batchSize {
			end := start + batchSize - 1
			if end > currentBlock {
				end = currentBlock
			}

			for blockNum := start; blockNum <= end; blockNum++ {
				if blockNum > safeHead {
					break
				}
				if ok := bt.enqueueJob(ctx, BlockJob{Number: blockNum, Source: blockSourceCatchup}); !ok {
					return
				}
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
		atomic.AddUint64(&bt.errorCount, 1)
		return
	}

	lastProcessed := bt.normalizeLastProcessed(ctx, currentBlock)
	if lastProcessed == 0 && currentBlock == 0 {
		return
	}

	safeHead := currentBlock
	if currentBlock > uint64(bt.config.Confirmations) {
		safeHead = currentBlock - uint64(bt.config.Confirmations)
	}

	// Process missing blocks
	for blockNum := lastProcessed + 1; blockNum <= currentBlock; blockNum++ {
		if blockNum > safeHead {
			break // Wait for more confirmations
		}

		if ok := bt.enqueueJob(ctx, BlockJob{Number: blockNum, Source: blockSourcePolling}); !ok {
			// Defer the rest of this polling pass; the next poll will retry from persisted progress.
			return
		}
	}
}

func (bt *BaseTracker) processBlockJob(ctx context.Context, job BlockJob, workerID int, workerKind string) {
	blockNumber := job.Number
	source := job.Source
	network := bt.processor.GetNetwork()

	bt.mu.Lock()
	if _, ok := bt.inFlightBlocks[blockNumber]; ok {
		bt.mu.Unlock()
		atomic.AddUint64(&bt.skippedAlreadyProcessed, 1)
		bt.logger.WithFields(logrus.Fields{
			"network":      network,
			"block_number": blockNumber,
			"source":       source,
		}).Debug("Block already in flight, skipping duplicate enqueue")
		return
	}
	bt.inFlightBlocks[blockNumber] = struct{}{}
	bt.mu.Unlock()

	releaseInFlight := func() {
		bt.mu.Lock()
		delete(bt.inFlightBlocks, blockNumber)
		bt.mu.Unlock()
	}

	processed, err := bt.storage.IsBlockProcessed(ctx, network, blockNumber)
	if err != nil {
		releaseInFlight()
		bt.logger.Errorf("Failed to check if block %d is processed: %v", blockNumber, err)
		atomic.AddUint64(&bt.errorCount, 1)
		return
	}
	if processed {
		releaseInFlight()
		atomic.AddUint64(&bt.skippedAlreadyProcessed, 1)
		if err := bt.storage.AdvanceHighWaterMark(ctx, network); err != nil {
			bt.logger.WithFields(logrus.Fields{
				"network":      network,
				"block_number": blockNumber,
				"source":       source,
				"error":        err,
			}).Error("Failed to advance high-water mark after already-processed block")
			atomic.AddUint64(&bt.errorCount, 1)
			return
		}
		bt.logger.WithFields(logrus.Fields{
			"network":      network,
			"block_number": blockNumber,
			"source":       source,
		}).Debug("Block already processed, advanced high-water mark")
		return
	}

	defer releaseInFlight()

	if err := bt.processBlockSafely(ctx, blockNumber, source); err != nil {
		if errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "context canceled") {
			return
		}
		bt.logger.WithFields(logrus.Fields{
			"network":      network,
			"block_number": blockNumber,
			"source":       source,
			"worker_id":    workerID,
			"worker_kind":  workerKind,
			"error":        err,
		}).Error("Failed to process block")
		atomic.AddUint64(&bt.errorCount, 1)
	}
}

// processBlockSafely processes a block with error handling and metrics
func (bt *BaseTracker) processBlockSafely(ctx context.Context, blockNumber uint64, source string) error {
	atomic.StoreUint64(&bt.lastProcessing, blockNumber)
	startTime := time.Now()
	network := bt.processor.GetNetwork()

	// Check confirmations
	currentBlock, err := bt.processor.GetCurrentBlockHeight(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		if strings.Contains(err.Error(), "context canceled") || strings.Contains(err.Error(), "use of closed network connection") {
			return nil
		}
		return fmt.Errorf("failed to get current block height: %w", err)
	}

	if currentBlock < blockNumber {
		bt.logger.WithFields(logrus.Fields{
			"network":       network,
			"block_number":  blockNumber,
			"current_block": currentBlock,
			"source":        source,
			"requeue_delay": bt.requeueDelay().String(),
		}).Debug("Block is ahead of current tip, requeueing")
		bt.requeueBlock(ctx, blockNumber)
		return nil
	}

	confirmations := currentBlock - blockNumber
	if confirmations < uint64(bt.config.Confirmations) {
		bt.logger.WithFields(logrus.Fields{
			"network":                network,
			"block_number":           blockNumber,
			"current_block":          currentBlock,
			"confirmations":          confirmations,
			"required_confirmations": bt.config.Confirmations,
			"source":                 source,
			"requeue_delay":          bt.requeueDelay().String(),
		}).Debug("Block does not have enough confirmations, requeueing")
		bt.requeueBlock(ctx, blockNumber)
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

		// Best-effort: drop cached block now that we're done (avoid cache growth)
		_ = bt.storage.DeleteCache(ctx, fmt.Sprintf("%s:block:%d", network, blockNumber))

		// Advance high-water mark only via contiguous processed bits.
		// This prevents out-of-order block completion from skipping unprocessed heights.
		if err := bt.storage.AdvanceHighWaterMark(ctx, network); err != nil {
			bt.logger.Errorf("Failed to advance high-water mark: %v", err)
		}

		// Update metrics
		atomic.AddUint64(&bt.processedBlocks, 1)

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

func (bt *BaseTracker) requeueBlock(ctx context.Context, blockNumber uint64) {
	delay := bt.requeueDelay()
	time.AfterFunc(delay, func() {
		job := BlockJob{Number: blockNumber, Source: blockSourceRequeue}
		select {
		case <-ctx.Done():
			return
		case <-bt.stopCh:
			return
		case bt.realtimeCh <- job:
			atomic.StoreUint64(&bt.lastEnqueued, blockNumber)
			bt.logger.WithFields(logrus.Fields{
				"network":       bt.processor.GetNetwork(),
				"block_number":  blockNumber,
				"source":        job.Source,
				"queue":         "realtime",
				"requeue_delay": delay.String(),
			}).Debug("Requeued block")
		default:
			bt.logger.WithFields(logrus.Fields{
				"network":       bt.processor.GetNetwork(),
				"block_number":  blockNumber,
				"requeue_delay": delay.String(),
				"queue":         "realtime",
				"queue_len":     len(bt.realtimeCh),
				"queue_cap":     cap(bt.realtimeCh),
			}).Warn("Realtime queue full, requeue deferred to polling")
		}
	})
}

func (bt *BaseTracker) requeueDelay() time.Duration {
	delay := bt.config.RequeueDelay
	if delay <= 0 {
		// Five seconds gives websocket/polling tips time to converge without creating a hot requeue loop.
		delay = 5 * time.Second
	}
	return delay
}

// reportHealth logs performance metrics
func (bt *BaseTracker) reportHealth() {
	uptime := time.Since(bt.startTime)
	processedBlocks := atomic.LoadUint64(&bt.processedBlocks)
	processedTxs := atomic.LoadUint64(&bt.processedTxs)
	errorCount := atomic.LoadUint64(&bt.errorCount)
	blocksPerSecond := float64(processedBlocks) / uptime.Seconds()
	txsPerSecond := float64(processedTxs) / uptime.Seconds()

	var (
		lastProcessed uint64
		currentBlock  uint64
		safeHead      uint64
		blockGap      uint64
	)
	lastProcessed, _ = bt.storage.GetLastProcessedBlock(context.Background(), bt.processor.GetNetwork())
	if cb, err := bt.processor.GetCurrentBlockHeight(context.Background()); err == nil {
		currentBlock = cb
		if currentBlock > uint64(bt.config.Confirmations) {
			safeHead = currentBlock - uint64(bt.config.Confirmations)
		}
		if safeHead > lastProcessed {
			blockGap = safeHead - lastProcessed
		}
	} else {
		bt.logger.Warnf("Failed to get current block height for health report: %v", err)
	}

	const gapWarnThreshold = 20
	const gapWarnConsecutiveThreshold = 3
	if blockGap > gapWarnThreshold {
		atomic.AddUint64(&bt.blockGapConsecutive, 1)
	} else {
		atomic.StoreUint64(&bt.blockGapConsecutive, 0)
	}
	gapConsecutive := atomic.LoadUint64(&bt.blockGapConsecutive)
	if blockGap > gapWarnThreshold && (gapConsecutive == gapWarnConsecutiveThreshold || gapConsecutive%10 == 0) {
		bt.logger.WithFields(logrus.Fields{
			"network":            bt.processor.GetNetwork(),
			"block_gap":          blockGap,
			"gap_threshold":      gapWarnThreshold,
			"gap_consecutive":    gapConsecutive,
			"last_processed":     lastProcessed,
			"safe_head":          safeHead,
			"realtime_queue_len": len(bt.realtimeCh),
			"realtime_queue_cap": cap(bt.realtimeCh),
			"catchup_queue_len":  len(bt.catchupCh),
			"catchup_queue_cap":  cap(bt.catchupCh),
		}).Warn("Block gap remains high")
	}

	monitoring.SetTrackerSnapshot(monitoring.TrackerSnapshot{
		Network:            string(bt.processor.GetNetwork()),
		BlockGap:           blockGap,
		CurrentBlockHeight: currentBlock,
		SafeHead:           safeHead,
		LastProcessedBlock: lastProcessed,
		BlockQueueLen:      len(bt.realtimeCh) + len(bt.catchupCh),
		BlockQueueCap:      cap(bt.realtimeCh) + cap(bt.catchupCh),
		ProcessedBlocks:    processedBlocks,
		ProcessedTxs:       processedTxs,
		ErrorCount:         errorCount,
		SkippedChannelFull: atomic.LoadUint64(&bt.skippedChannelFull),
		SkippedProcessed:   atomic.LoadUint64(&bt.skippedAlreadyProcessed),
		BlocksPerSecond:    blocksPerSecond,
		TxsPerSecond:       txsPerSecond,
		UptimeSeconds:      uptime.Seconds(),
	})

	bt.logger.WithFields(logrus.Fields{
		"network":               bt.processor.GetNetwork(),
		"uptime":                uptime,
		"processed_blocks":      processedBlocks,
		"processed_txs":         processedTxs,
		"error_count":           errorCount,
		"blocks_per_second":     blocksPerSecond,
		"txs_per_second":        txsPerSecond,
		"last_block_height":     lastProcessed,
		"current_block_height":  currentBlock,
		"safe_head":             safeHead,
		"block_gap":             blockGap,
		"confirmations":         bt.config.Confirmations,
		"block_queue_len":       len(bt.realtimeCh) + len(bt.catchupCh),
		"block_queue_cap":       cap(bt.realtimeCh) + cap(bt.catchupCh),
		"realtime_queue_len":    len(bt.realtimeCh),
		"realtime_queue_cap":    cap(bt.realtimeCh),
		"catchup_queue_len":     len(bt.catchupCh),
		"catchup_queue_cap":     cap(bt.catchupCh),
		"last_enqueued_block":   atomic.LoadUint64(&bt.lastEnqueued),
		"last_dequeued_block":   atomic.LoadUint64(&bt.lastDequeued),
		"last_processing_block": atomic.LoadUint64(&bt.lastProcessing),
		"skipped_channel_full":  atomic.LoadUint64(&bt.skippedChannelFull),
		"skipped_processed":     atomic.LoadUint64(&bt.skippedAlreadyProcessed),
	}).Debug("Tracker health report")
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

// PublishWithdrawal publishes a withdrawal event
func (bt *BaseTracker) PublishWithdrawal(ctx context.Context, withdrawal *types.WalletWithdrawal) error {
	if err := bt.publisher.PublishWithdrawal(ctx, withdrawal); err != nil {
		return fmt.Errorf("failed to publish withdrawal: %w", err)
	}

	bt.logger.WithFields(logrus.Fields{
		"network":      withdrawal.Network,
		"tx_hash":      withdrawal.TxHash,
		"wallet_id":    withdrawal.WalletID,
		"amount":       withdrawal.Amount,
		"currency":     withdrawal.Currency,
		"block_number": withdrawal.BlockNumber,
	}).Info("Withdrawal detected and published")

	return nil
}

// IncrementTxCount increments the processed transaction counter
func (bt *BaseTracker) IncrementTxCount(count uint64) {
	atomic.AddUint64(&bt.processedTxs, count)
}
