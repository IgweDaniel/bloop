package ethereum

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/igwedaniel/bloop/internal/blockchain/base"
	"github.com/igwedaniel/bloop/internal/config"
	"github.com/igwedaniel/bloop/internal/storage"
	bloopTypes "github.com/igwedaniel/bloop/internal/types"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"
)

// USDT ABI
const usdtABI = `[{"constant":false,"inputs":[{"name":"_to","type":"address"},{"name":"_value","type":"uint256"}],"name":"transfer","type":"function"},{"anonymous":false,"inputs":[{"indexed":true,"name":"from","type":"address"},{"indexed":true,"name":"to","type":"address"},{"indexed":false,"name":"value","type":"uint256"}],"name":"Transfer","type":"event"}]`

var (
	transferEventSignature = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))
)

type EthereumProcessor struct {
	client       *EthereumClient
	wsClient     *ethclient.Client
	ominiClient  *ethclient.Client // Dedicated client for block fetching (like TypeScript ominiProvider)
	storage      storage.Storage
	config       *config.EthereumConfig
	logger       *logrus.Logger
	usdtContract common.Address
	usdtABI      abi.ABI
	baseTracker  *base.BaseTracker

	network      bloopTypes.BlockchainType
	rpcSemaphore *semaphore.Weighted
}

func NewEthereumProcessor(
	cfg *config.EthereumConfig,
	storage storage.Storage,
	logger *logrus.Logger,
	network bloopTypes.BlockchainType,
) (*EthereumProcessor, error) {
	usdtAbi, err := abi.JSON(strings.NewReader(usdtABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse USDT ABI: %w", err)
	}

	processor := &EthereumProcessor{
		storage:      storage,
		config:       cfg,
		logger:       logger,
		usdtContract: common.HexToAddress(cfg.USDTContract),
		usdtABI:      usdtAbi,
		network:      network,
		rpcSemaphore: semaphore.NewWeighted(50),
	}

	return processor, nil
}

func (ep *EthereumProcessor) SetBaseTracker(baseTracker *base.BaseTracker) {
	ep.baseTracker = baseTracker
}

func (ep *EthereumProcessor) GetNetwork() bloopTypes.BlockchainType {
	return ep.network
}

// InitializeProviders sets up RPC connections and providers
func (ep *EthereumProcessor) InitializeProviders(ctx context.Context) error {
	client, err := NewEthereumClient(ep.config, ep.logger)
	if err != nil {
		return fmt.Errorf("failed to create %s client: %w", ep.network, err)
	}

	ep.client = client

	ep.logger.Info("omini RPC URL: ", ep.config.OminiRPCURL)
	// Initialize dedicated omini client for block fetching
	if ep.config.OminiRPCURL != "" {
		ominiClient, err := ethclient.Dial(ep.config.OminiRPCURL)
		if err != nil {
			ep.logger.Warnf("Failed to create omini client, using regular client: %v", err)
		} else {
			ep.ominiClient = ominiClient
			ep.logger.Info("Omini RPC client initialized for block fetching")
		}
	}

	ep.logger.Infof("%s providers initialized successfully", ep.network)
	return nil
}

// CleanupProviders closes connections and cleans up resources
func (ep *EthereumProcessor) CleanupProviders() error {
	if ep.wsClient != nil {
		ep.wsClient.Close()
	}
	if ep.ominiClient != nil {
		ep.ominiClient.Close()
	}
	if ep.client != nil {
		ep.client.Close()
	}
	return nil
}

func (ep *EthereumProcessor) GetCurrentBlockHeight(ctx context.Context) (uint64, error) {
	return ep.client.BlockNumber(ctx)
}

func (ep *EthereumProcessor) SubscribeToNewBlocks(ctx context.Context, blockCh chan<- uint64) error {
	if ep.config.WSURL == "" {
		return fmt.Errorf("no WebSocket URL configured")
	}

	wsClient, err := ethclient.Dial(ep.config.WSURL)
	if err != nil {
		return fmt.Errorf("failed to connect to WebSocket: %w", err)
	}

	ep.wsClient = wsClient
	headers := make(chan *types.Header, 100)

	sub, err := wsClient.SubscribeNewHead(ctx, headers)
	if err != nil {
		wsClient.Close()
		return fmt.Errorf("failed to subscribe to new heads: %w", err)
	}

	ep.logger.Info("WebSocket subscription established")

	go func() {
		defer sub.Unsubscribe()
		defer wsClient.Close()

		for {
			select {
			case <-ctx.Done():
				return
			case err := <-sub.Err():
				ep.logger.Errorf("WebSocket subscription error: %v", err)
				return
			case header := <-headers:
				if header != nil {
					select {
					case blockCh <- header.Number.Uint64():
					case <-ctx.Done():
						return
					default:
						// Channel full, skip this block
					}
				}
			}
		}
	}()

	return nil
}

func (ep *EthereumProcessor) ProcessBlock(ctx context.Context, blockNumber uint64) (bool, error) {
	// Add recovery for panics that might occur with unsupported transaction types
	defer func() {
		if r := recover(); r != nil {
			ep.logger.Errorf("Panic recovered while processing block %d: %v", blockNumber, r)
		}
	}()

	// Get block with transactions (cached omini client response)
	block, err := ep.getBlockTransactions(ctx, blockNumber)

	if err != nil {
		// Check if this is a transaction type error and make it more informative
		if strings.Contains(err.Error(), "transaction type not supported") {
			ep.logger.Warnf("Skipping block %d due to unsupported transaction types: %v", blockNumber, err)
			return true, nil // Mark as processed to avoid retrying
		}
		return false, fmt.Errorf("failed to get block %d: %w", blockNumber, err)
	}

	if block == nil {
		return false, fmt.Errorf("block %d not found", blockNumber)
	}

	// Get processed transactions for this block
	processedTxs, err := ep.storage.GetProcessedTransactions(ctx, ep.network, blockNumber)
	if err != nil {
		return false, fmt.Errorf("failed to get processed transactions: %w", err)
	}

	processedTxMap := make(map[string]bool)
	for _, txHash := range processedTxs {
		processedTxMap[txHash] = true
	}

	// Process transactions in batches
	transactions := block.Transactions()
	batchSize := ep.config.BatchSize

	for i := 0; i < len(transactions); i += batchSize {
		end := i + batchSize
		if end > len(transactions) {
			end = len(transactions)
		}

		batch := transactions[i:end]
		if err := ep.processBatch(ctx, batch, blockNumber, processedTxMap); err != nil {
			return false, fmt.Errorf("failed to process batch %d-%d: %w", i, end-1, err)
		}
	}

	// Clean up progress tracking
	if err := ep.storage.ClearBlockProgress(ctx, ep.network, blockNumber); err != nil {
		ep.logger.Errorf("Failed to clear block progress: %v", err)
	}

	// Update transaction count in base tracker
	if ep.baseTracker != nil {
		ep.baseTracker.IncrementTxCount(uint64(len(transactions)))
	}

	return true, nil
}

func (ep *EthereumProcessor) processBatch(ctx context.Context, batch types.Transactions, blockNumber uint64, processedTxMap map[string]bool) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(batch))

	for _, tx := range batch {
		// Skip if already processed
		if processedTxMap[tx.Hash().Hex()] {
			continue
		}

		wg.Add(1)
		go func(transaction *types.Transaction) {
			defer wg.Done()

			if err := ep.rpcSemaphore.Acquire(ctx, 1); err != nil {
				errCh <- err
				return
			}
			defer ep.rpcSemaphore.Release(1)

			if err := ep.processTransaction(ctx, transaction, blockNumber); err != nil {
				ep.logger.Errorf("Failed to process transaction %s: %v", transaction.Hash().Hex(), err)
				// Don't return error for individual transaction failures
			}

			// Mark transaction as processed
			if err := ep.storage.AddProcessedTransaction(ctx, ep.network, blockNumber, transaction.Hash().Hex()); err != nil {
				ep.logger.Errorf("Failed to mark transaction as processed: %v", err)
			}
		}(tx)
	}

	wg.Wait()
	close(errCh)

	// Check for any critical errors
	for err := range errCh {
		if err != nil {
			return err
		}
	}

	return nil
}

func (ep *EthereumProcessor) processTransaction(ctx context.Context, tx *types.Transaction, blockNumber uint64) error {
	// Optionally skip native processing (useful for BSC where we focus on tokens)
	if !ep.config.SkipNative {
		if err := ep.processETHTransaction(ctx, tx, blockNumber); err != nil {
			ep.logger.Errorf("Failed to process native transaction %s: %v", tx.Hash().Hex(), err)
		}
	}

	if err := ep.processUSDTTransaction(ctx, tx, blockNumber); err != nil {
		ep.logger.Errorf("Failed to process USDT transaction %s: %v", tx.Hash().Hex(), err)
	}

	return nil
}

// processETHTransaction processes native ETH transactions
func (ep *EthereumProcessor) processETHTransaction(ctx context.Context, tx *types.Transaction, blockNumber uint64) error {
	// Skip if no value or no recipient
	if tx.Value().Cmp(big.NewInt(0)) == 0 || tx.To() == nil {
		return nil
	}

	switch tx.Type() {
	case types.LegacyTxType, types.AccessListTxType, types.DynamicFeeTxType:
	case types.BlobTxType:
		ep.logger.Debugf("Skipping blob transaction %s (EIP-4844)", tx.Hash().Hex())
		return nil
	default:
		ep.logger.Warnf("Skipping unsupported transaction type %d in tx %s", tx.Type(), tx.Hash().Hex())
		return nil
	}

	walletID, isWatched, err := ep.storage.IsWatchedWallet(ctx, ep.network, tx.To().Hex())
	if err != nil {
		return fmt.Errorf("failed to check watched wallet: %w", err)
	}
	if !isWatched {
		return nil
	}

	receipt, err := ep.client.TransactionReceipt(ctx, tx.Hash())
	if err != nil {
		return fmt.Errorf("failed to get transaction receipt: %w", err)
	}

	if receipt.Status == 0 {
		return nil
	}

	gasUsed := new(big.Int).SetUint64(receipt.GasUsed)
	var gasPrice *big.Int

	// Handle different transaction types for gas price calculation
	switch tx.Type() {
	case types.DynamicFeeTxType, types.BlobTxType:
		// EIP-1559 transactions use effective gas price from receipt
		gasPrice = receipt.EffectiveGasPrice
		if gasPrice == nil {
			gasPrice = big.NewInt(0)
		}
	default:
		// Legacy and access list transactions use gas price from transaction
		gasPrice = tx.GasPrice()
		if gasPrice == nil {
			gasPrice = big.NewInt(0)
		}
	}

	networkFee := new(big.Int).Mul(gasUsed, gasPrice)

	// Create deposit event
	deposit := &bloopTypes.WalletDeposit{
		TxHash:        tx.Hash().Hex(),
		WalletID:      walletID,
		WalletAddress: tx.To().Hex(),
		FromAddress:   ep.getFromAddress(tx),
		Amount:        formatEther(tx.Value()),
		Currency:      bloopTypes.ETH,
		Network:       ep.network,
		BlockNumber:   blockNumber,
		Confirmations: 1,
		Timestamp:     ep.BlockToTimestamp(ctx, blockNumber),
		NetworkFee:    formatEther(networkFee),
		Status:        bloopTypes.StatusConfirmed,
		RawData: map[string]interface{}{
			"transaction": tx,
			"receipt":     receipt,
		},
	}

	if ep.baseTracker != nil {
		return ep.baseTracker.PublishDeposit(ctx, deposit)
	}

	return nil
}

// processUSDTTransaction processes USDT token transactions
func (ep *EthereumProcessor) processUSDTTransaction(ctx context.Context, tx *types.Transaction, blockNumber uint64) error {
	// Skip if not USDT contract
	if tx.To() == nil || tx.To().Hex() != ep.usdtContract.Hex() {
		return nil
	}

	switch tx.Type() {
	case types.LegacyTxType, types.AccessListTxType, types.DynamicFeeTxType:
	case types.BlobTxType:
		ep.logger.Debugf("Processing blob transaction %s for USDT transfers", tx.Hash().Hex())
	default:
		ep.logger.Warnf("Skipping unsupported transaction type %d in tx %s", tx.Type(), tx.Hash().Hex())
		return nil
	}

	receipt, err := ep.client.TransactionReceipt(ctx, tx.Hash())
	if err != nil {
		return fmt.Errorf("failed to get transaction receipt: %w", err)
	}

	// Skip failed transactions
	if receipt.Status == 0 {
		return nil
	}

	// Parse transfer events
	for _, log := range receipt.Logs {
		if len(log.Topics) == 0 || log.Topics[0] != transferEventSignature {
			continue
		}

		// Parse the transfer event
		event, err := ep.usdtABI.Unpack("Transfer", log.Data)
		if err != nil {
			ep.logger.Errorf("Failed to unpack Transfer event: %v", err)
			continue
		}

		if len(log.Topics) < 3 {
			continue
		}

		from := common.HexToAddress(log.Topics[1].Hex())
		to := common.HexToAddress(log.Topics[2].Hex())

		// Check if recipient is a watched wallet
		walletID, isWatched, err := ep.storage.IsWatchedWallet(ctx, ep.network, to.Hex())
		if err != nil {
			ep.logger.Errorf("Failed to check watched wallet: %v", err)
			continue
		}
		if !isWatched {
			continue
		}

		// Extract amount (USDT has 6 decimals)
		var amount *big.Int
		if len(event) > 0 {
			if val, ok := event[0].(*big.Int); ok {
				amount = val
			}
		}
		if amount == nil {
			continue
		}

		gasUsed := new(big.Int).SetUint64(receipt.GasUsed)
		var gasPrice *big.Int

		switch tx.Type() {
		case types.DynamicFeeTxType, types.BlobTxType:
			gasPrice = receipt.EffectiveGasPrice
			if gasPrice == nil {
				gasPrice = big.NewInt(0)
			}
		default:
			gasPrice = tx.GasPrice()
			if gasPrice == nil {
				gasPrice = big.NewInt(0)
			}
		}

		networkFee := new(big.Int).Mul(gasUsed, gasPrice)

		// Create deposit event
		deposit := &bloopTypes.WalletDeposit{
			TxHash:        tx.Hash().Hex(),
			WalletID:      walletID,
			WalletAddress: to.Hex(),
			FromAddress:   from.Hex(),
			// FIXIME: take from config, the decimals
			Amount:        formatToken(amount, ep.config.USDTDecimals),
			Currency:      bloopTypes.USDT,
			Network:       ep.network,
			BlockNumber:   blockNumber,
			Confirmations: 1,
			Timestamp:     ep.BlockToTimestamp(ctx, blockNumber),
			NetworkFee:    formatEther(networkFee),
			Status:        bloopTypes.StatusConfirmed,
			RawData: map[string]interface{}{
				"transaction": tx,
				"receipt":     receipt,
				"log":         log,
			},
		}

		// Publish deposit event using base tracker
		if ep.baseTracker != nil {
			if err := ep.baseTracker.PublishDeposit(ctx, deposit); err != nil {
				ep.logger.Errorf("Failed to publish USDT deposit: %v", err)
				continue
			}
		}
	}

	return nil
}

func (ep *EthereumProcessor) getFromAddress(tx *types.Transaction) string {
	signer := types.NewEIP155Signer(tx.ChainId())
	from, err := types.Sender(signer, tx)
	if err != nil {
		return ""
	}
	return from.Hex()
}

func formatEther(wei *big.Int) string {
	if wei == nil {
		return "0"
	}
	return decimal.NewFromBigInt(wei, -18).StringFixed(18)
}

// getBlockTransactions fetches block with a short-lived cache to reduce quoted omini calls
func (ep *EthereumProcessor) getBlockTransactions(ctx context.Context, blockNumber uint64) (*types.Block, error) {
	cacheKey := fmt.Sprintf("ETH:block:%d", blockNumber)

	// Try cache first to avoid repeated omini calls on retries
	var cached types.Block
	if err := ep.storage.GetCache(ctx, cacheKey, &cached); err == nil {
		ep.logger.Debugf("Block cache HIT for block %d", blockNumber)
		return &cached, nil
	}

	ep.logger.Debugf("Block cache MISS for block %d", blockNumber)

	var block *types.Block
	var err error

	if ep.ominiClient != nil {
		// Use dedicated omini client for block fetching (supports all transaction types)
		// This is the expensive quoted RPC call, but blocks are only fetched once per processing
		block, err = ep.ominiClient.BlockByNumber(ctx, big.NewInt(int64(blockNumber)))
		if err != nil {
			ep.logger.Warnf("Omini client failed for block %d, falling back to regular client: %v", blockNumber, err)
			// Fall back to regular client
			block, err = ep.client.BlockByNumber(ctx, big.NewInt(int64(blockNumber)))
		}
	} else {
		// Use regular client
		block, err = ep.client.BlockByNumber(ctx, big.NewInt(int64(blockNumber)))
	}

	if err != nil {
		return nil, err
	}

	// Store in cache with a modest TTL so immediate retries don't hit omini again
	if err := ep.storage.SetCache(ctx, cacheKey, block, 30*time.Minute); err != nil {
		ep.logger.Warnf("Failed to cache block %d: %v", blockNumber, err)
	}

	return block, nil
}

func formatToken(amount *big.Int, decimals int32) string {
	if amount == nil {
		return "0"
	}
	if decimals < 0 {
		decimals = 0
	}
	return decimal.NewFromBigInt(amount, -decimals).StringFixed(decimals)
}

func (ep *EthereumProcessor) getHeaderByNumber(ctx context.Context, blockNumber uint64) (*types.Header, error) {
	return ep.client.HeaderByNumber(ctx, big.NewInt(int64(blockNumber)))
}

// BlockToTimestamp returns the timestamp (as time.Time) for a given block number.
// It fetches the block using the processor's block fetching logic.
func (ep *EthereumProcessor) BlockToTimestamp(ctx context.Context, blockNumber uint64) time.Time {
	block, err := ep.getHeaderByNumber(ctx, blockNumber)
	if err != nil {
		ep.logger.Errorf("Failed to get header by number %d: %v", blockNumber, err)
		return time.Now()
	}
	// Block time is in seconds since epoch (uint64)
	return time.Unix(int64(block.Time), 0)
}
