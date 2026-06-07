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
	"golang.org/x/time/rate"
)

const erc20ABI = `[{"constant":false,"inputs":[{"name":"_to","type":"address"},{"name":"_value","type":"uint256"}],"name":"transfer","type":"function"},{"anonymous":false,"inputs":[{"indexed":true,"name":"from","type":"address"},{"indexed":true,"name":"to","type":"address"},{"indexed":false,"name":"value","type":"uint256"}],"name":"Transfer","type":"event"}]`

var (
	transferEventSignature = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))
)

type EthereumProcessor struct {
	client         *EthereumClient
	wsClient       *ethclient.Client
	ominiClients   []*ethclient.Client // Multiple dedicated clients for block fetching with rotation
	ominiLimiters  []*rate.Limiter     // Rate limiters for each omini client
	ominiIndex     int                 // Current index for round-robin rotation
	ominiMu        sync.RWMutex        // Mutex for omini client rotation
	storage        storage.Storage
	config         *config.EVMConfig
	logger         *logrus.Logger
	tokenContracts map[common.Address]config.TokenConfig
	erc20ABI       abi.ABI
	baseTracker    *base.BaseTracker

	nativeCurrency bloopTypes.Currency
	network        bloopTypes.BlockchainType
	rpcSemaphore   *semaphore.Weighted
}

func NewEthereumProcessor(
	cfg *config.EVMConfig,
	storage storage.Storage,
	logger *logrus.Logger,
) (*EthereumProcessor, error) {
	erc20Abi, err := abi.JSON(strings.NewReader(erc20ABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ERC20 ABI: %w", err)
	}
	tokenContracts, err := buildEVMTokenMap(cfg.Tokens)
	if err != nil {
		return nil, err
	}
	if cfg.Network == "" {
		return nil, fmt.Errorf("EVM network is required")
	}
	if cfg.NativeCurrency == "" {
		return nil, fmt.Errorf("native currency is required for EVM network %s", cfg.Network)
	}

	processor := &EthereumProcessor{
		storage:        storage,
		config:         cfg,
		logger:         logger,
		tokenContracts: tokenContracts,
		erc20ABI:       erc20Abi,
		nativeCurrency: cfg.NativeCurrency,
		network:        cfg.Network,
		rpcSemaphore:   semaphore.NewWeighted(20), // Reduced from 50 for lower memory usage
	}

	return processor, nil
}

func buildEVMTokenMap(tokens []config.TokenConfig) (map[common.Address]config.TokenConfig, error) {
	tokenContracts := make(map[common.Address]config.TokenConfig, len(tokens))
	for _, token := range tokens {
		if strings.TrimSpace(token.Contract) == "" {
			return nil, fmt.Errorf("token %s has empty contract", token.Currency)
		}
		if strings.TrimSpace(token.Currency) == "" {
			return nil, fmt.Errorf("token contract %s has empty currency", token.Contract)
		}
		if !common.IsHexAddress(token.Contract) {
			return nil, fmt.Errorf("invalid EVM token contract address %q", token.Contract)
		}
		tokenContracts[common.HexToAddress(token.Contract)] = token
	}
	return tokenContracts, nil
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

	// Build list of omini URLs (support both old and new config format)
	var ominiURLs []string
	if len(ep.config.OminiRPCURLs) > 0 {
		ominiURLs = ep.config.OminiRPCURLs
	} else if ep.config.OminiRPCURL != "" {
		// Backwards compatibility with single URL
		ominiURLs = []string{ep.config.OminiRPCURL}
	}

	// Initialize multiple dedicated omini clients for block fetching with rotation
	if len(ominiURLs) > 0 {
		// Get rate limit config
		rps := ep.config.OminiRequestsPerSecond
		if rps <= 0 {
			rps = 5 // Default to 5 RPS
		}
		burst := ep.config.OminiRequestsBurst
		if burst <= 0 {
			burst = 10 // Default to 10 burst
		}

		ep.ominiClients = make([]*ethclient.Client, 0, len(ominiURLs))
		ep.ominiLimiters = make([]*rate.Limiter, 0, len(ominiURLs))

		for i, url := range ominiURLs {
			ominiClient, err := ethclient.Dial(url)
			if err != nil {
				ep.logger.Warnf("Failed to create omini client #%d (%s): %v", i+1, url, err)
				continue
			}

			// Create a dedicated rate limiter for this omini client
			limiter := rate.NewLimiter(rate.Limit(rps), burst)

			ep.ominiClients = append(ep.ominiClients, ominiClient)
			ep.ominiLimiters = append(ep.ominiLimiters, limiter)
			ep.logger.Infof("Omini RPC client #%d initialized with %d RPS (burst: %d): %s",
				len(ep.ominiClients), rps, burst, url)
		}

		if len(ep.ominiClients) == 0 {
			ep.logger.Warn("No omini clients available, will use regular client for block fetching")
		} else {
			ep.logger.Infof("Initialized %d omini RPC clients with rotation and rate limiting", len(ep.ominiClients))
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
	// Close all omini clients
	for _, ominiClient := range ep.ominiClients {
		if ominiClient != nil {
			ominiClient.Close()
		}
	}
	if ep.client != nil {
		ep.client.Close()
	}
	return nil
}

func (ep *EthereumProcessor) GetCurrentBlockHeight(ctx context.Context) (uint64, error) {
	return ep.client.BlockNumber(ctx)
}

// getNextOminiClient returns the next omini client and its rate limiter using round-robin rotation
func (ep *EthereumProcessor) getNextOminiClient() (*ethclient.Client, *rate.Limiter) {
	if len(ep.ominiClients) == 0 {
		return nil, nil
	}

	ep.ominiMu.Lock()
	defer ep.ominiMu.Unlock()

	client := ep.ominiClients[ep.ominiIndex]
	limiter := ep.ominiLimiters[ep.ominiIndex]
	ep.ominiIndex = (ep.ominiIndex + 1) % len(ep.ominiClients)

	return client, limiter
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
	headers := make(chan *types.Header, 10) // Reduced from 100 for lower memory usage

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
			ep.logger.WithFields(logrus.Fields{
				"network":      ep.network,
				"block_number": blockNumber,
				"panic":        r,
			}).Error("Panic recovered while processing block")
		}
	}()

	// Get block with transactions.
	block, err := ep.getBlockTransactions(ctx, blockNumber)

	if err != nil {
		// Check if this is a transaction type error and make it more informative
		if strings.Contains(err.Error(), "transaction type not supported") {
			ep.logger.WithFields(logrus.Fields{
				"network":      ep.network,
				"block_number": blockNumber,
				"error":        err,
			}).Warn("Skipping block due to unsupported transaction types")
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

	switch tx.Type() {
	case types.LegacyTxType, types.AccessListTxType, types.DynamicFeeTxType:
	case types.BlobTxType:
		ep.logger.Debugf("Processing blob transaction %s for token transfers", tx.Hash().Hex())
	default:
		ep.logger.WithFields(logrus.Fields{
			"network":      ep.network,
			"block_number": blockNumber,
			"tx_hash":      tx.Hash().Hex(),
			"tx_type":      tx.Type(),
		}).Debug("Skipping unsupported transaction type")
		return nil
	}

	relevance, err := ep.transactionRelevance(ctx, tx)
	if err != nil {
		return fmt.Errorf("failed to check transaction relevance: %w", err)
	}
	if !relevance.native && !relevance.token {
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

	// Optionally skip native processing when a network only needs token monitoring.
	if relevance.native {
		if err := ep.processNativeTransaction(ctx, tx, blockNumber, receipt); err != nil {
			ep.logger.Errorf("Failed to process native transaction %s: %v", tx.Hash().Hex(), err)
		}
	}

	if relevance.token {
		if err := ep.processTokenTransaction(ctx, tx, blockNumber, receipt); err != nil {
			ep.logger.Errorf("Failed to process token transaction %s: %v", tx.Hash().Hex(), err)
		}
	}

	return nil
}

type transactionRelevance struct {
	native bool
	token  bool
}

func (ep *EthereumProcessor) transactionRelevance(ctx context.Context, tx *types.Transaction) (transactionRelevance, error) {
	relevance := transactionRelevance{
		token: ep.isDirectTokenTransaction(tx),
	}

	if ep.config.SkipNative || !isPlainNativeTransfer(tx) {
		return relevance, nil
	}

	fromAddr := ep.getFromAddress(tx)
	if fromAddr != "" {
		_, isFromWatched, err := ep.isWatchedWallet(ctx, fromAddr)
		if err != nil {
			return relevance, fmt.Errorf("failed to check watched sender: %w", err)
		}
		if isFromWatched {
			relevance.native = true
			return relevance, nil
		}
	}

	_, isToWatched, err := ep.isWatchedWallet(ctx, tx.To().Hex())
	if err != nil {
		return relevance, fmt.Errorf("failed to check watched recipient: %w", err)
	}
	relevance.native = isToWatched

	return relevance, nil
}

func isPlainNativeTransfer(tx *types.Transaction) bool {
	return tx.To() != nil && tx.Value().Sign() > 0 && len(tx.Data()) == 0
}

func (ep *EthereumProcessor) isDirectTokenTransaction(tx *types.Transaction) bool {
	if tx.To() == nil || len(ep.tokenContracts) == 0 {
		return false
	}
	_, ok := ep.tokenContracts[*tx.To()]
	return ok
}

func (ep *EthereumProcessor) isWatchedWallet(ctx context.Context, address string) (string, bool, error) {
	return ep.storage.IsWatchedWallet(ctx, ep.network, strings.ToLower(address))
}

// processNativeTransaction processes native EVM transfers.
func (ep *EthereumProcessor) processNativeTransaction(ctx context.Context, tx *types.Transaction, blockNumber uint64, receipt *types.Receipt) error {
	// Skip if no value or no recipient
	if tx.Value().Cmp(big.NewInt(0)) == 0 || tx.To() == nil {
		return nil
	}

	fromAddr := ep.getFromAddress(tx)
	fromWalletID, isFromWatched, err := ep.isWatchedWallet(ctx, fromAddr)
	if err != nil {
		return fmt.Errorf("failed to check watched wallet: %w", err)
	}

	toWalletID, isToWatched, err := ep.isWatchedWallet(ctx, tx.To().Hex())
	if err != nil {
		return fmt.Errorf("failed to check watched wallet: %w", err)
	}

	if isFromWatched && !isToWatched {
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

		withdrawal := &bloopTypes.WalletWithdrawal{
			TxHash:        tx.Hash().Hex(),
			WalletID:      fromWalletID,
			WalletAddress: fromAddr,
			ToAddress:     tx.To().Hex(),
			Amount:        formatEther(tx.Value()),
			Currency:      ep.nativeCurrency,
			Network:       ep.network,
			BlockNumber:   blockNumber,
			Confirmations: 1,
			Timestamp:     ep.BlockToTimestamp(ctx, blockNumber),
			NetworkFee:    formatEther(networkFee),
			Status:        bloopTypes.StatusConfirmed,
		}

		if ep.baseTracker != nil {
			if err := ep.baseTracker.PublishWithdrawal(ctx, withdrawal); err != nil {
				ep.logger.Errorf("Failed to publish %s withdrawal: %v", ep.nativeCurrency, err)
			}
		}
	}

	walletID, isWatched := toWalletID, isToWatched
	if !isWatched {
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

	// Create deposit event (RawData omitted to reduce memory footprint)
	deposit := &bloopTypes.WalletDeposit{
		TxHash:        tx.Hash().Hex(),
		WalletID:      walletID,
		WalletAddress: tx.To().Hex(),
		FromAddress:   ep.getFromAddress(tx),
		Amount:        formatEther(tx.Value()),
		Currency:      ep.nativeCurrency,
		Network:       ep.network,
		BlockNumber:   blockNumber,
		Confirmations: 1,
		Timestamp:     ep.BlockToTimestamp(ctx, blockNumber),
		NetworkFee:    formatEther(networkFee),
		Status:        bloopTypes.StatusConfirmed,
	}

	if ep.baseTracker != nil {
		return ep.baseTracker.PublishDeposit(ctx, deposit)
	}

	return nil
}

// processTokenTransaction processes configured ERC20 token transfers.
func (ep *EthereumProcessor) processTokenTransaction(ctx context.Context, tx *types.Transaction, blockNumber uint64, receipt *types.Receipt) error {
	if len(ep.tokenContracts) == 0 {
		return nil
	}

	// Parse transfer events
	for _, log := range receipt.Logs {
		if len(log.Topics) == 0 || log.Topics[0] != transferEventSignature {
			continue
		}
		token, ok := ep.tokenContracts[log.Address]
		if !ok {
			continue
		}

		// Parse the transfer event
		event, err := ep.erc20ABI.Unpack("Transfer", log.Data)
		if err != nil {
			ep.logger.Errorf("Failed to unpack Transfer event: %v", err)
			continue
		}

		if len(log.Topics) < 3 {
			continue
		}

		from := common.HexToAddress(log.Topics[1].Hex())
		to := common.HexToAddress(log.Topics[2].Hex())

		fromWalletID, isFromWatched, err := ep.isWatchedWallet(ctx, from.Hex())
		if err != nil {
			ep.logger.Errorf("Failed to check watched wallet: %v", err)
			continue
		}

		toWalletID, isToWatched, err := ep.isWatchedWallet(ctx, to.Hex())
		if err != nil {
			ep.logger.Errorf("Failed to check watched wallet: %v", err)
			continue
		}

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

		if isFromWatched && !isToWatched {
			withdrawal := &bloopTypes.WalletWithdrawal{
				TxHash:        tx.Hash().Hex(),
				WalletID:      fromWalletID,
				WalletAddress: from.Hex(),
				ToAddress:     to.Hex(),
				Amount:        formatToken(amount, token.Decimals),
				Currency:      bloopTypes.Currency(token.Currency),
				Network:       ep.network,
				BlockNumber:   blockNumber,
				Confirmations: 1,
				Timestamp:     ep.BlockToTimestamp(ctx, blockNumber),
				NetworkFee:    formatEther(networkFee),
				Status:        bloopTypes.StatusConfirmed,
			}

			if ep.baseTracker != nil {
				if err := ep.baseTracker.PublishWithdrawal(ctx, withdrawal); err != nil {
					ep.logger.Errorf("Failed to publish %s withdrawal: %v", token.Currency, err)
				}
			}
		}

		if !isToWatched {
			continue
		}

		// Create deposit event (RawData omitted to reduce memory footprint)
		deposit := &bloopTypes.WalletDeposit{
			TxHash:        tx.Hash().Hex(),
			WalletID:      toWalletID,
			WalletAddress: to.Hex(),
			FromAddress:   from.Hex(),
			Amount:        formatToken(amount, token.Decimals),
			Currency:      bloopTypes.Currency(token.Currency),
			Network:       ep.network,
			BlockNumber:   blockNumber,
			Confirmations: 1,
			Timestamp:     ep.BlockToTimestamp(ctx, blockNumber),
			NetworkFee:    formatEther(networkFee),
			Status:        bloopTypes.StatusConfirmed,
		}

		// Publish deposit event using base tracker
		if ep.baseTracker != nil {
			if err := ep.baseTracker.PublishDeposit(ctx, deposit); err != nil {
				ep.logger.Errorf("Failed to publish %s deposit: %v", token.Currency, err)
				continue
			}
		}
	}

	return nil
}

func (ep *EthereumProcessor) getFromAddress(tx *types.Transaction) string {
	signer := types.LatestSignerForChainID(tx.ChainId())
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

// getBlockTransactions fetches block data with a short-lived cache for retry scenarios.
func (ep *EthereumProcessor) getBlockTransactions(ctx context.Context, blockNumber uint64) (*types.Block, error) {
	cacheKey := fmt.Sprintf("%s:block:%d", ep.network, blockNumber)

	// Try cache first to avoid repeated block fetches on retries.
	var cached types.Block
	if err := ep.storage.GetCache(ctx, cacheKey, &cached); err == nil {
		ep.logger.WithFields(logrus.Fields{
			"network":      ep.network,
			"block_number": blockNumber,
		}).Debug("Block cache HIT")
		return &cached, nil
	}

	ep.logger.WithFields(logrus.Fields{
		"network":      ep.network,
		"block_number": blockNumber,
	}).Debug("Block cache MISS")

	var block *types.Block
	var err error

	// Check if we're in light mode
	if ep.config.BlockFetchMode == "light" {
		ep.logger.WithFields(logrus.Fields{
			"network":      ep.network,
			"block_number": blockNumber,
		}).Debug("Using light block fetch mode")
		// Use GetBlockVerbose which fetches transactions individually with RPC rotation
		blockVerbose, err := ep.client.GetBlockVerbose(ctx, blockNumber)
		if err != nil {
			return nil, fmt.Errorf("failed to get block verbose: %w", err)
		}

		// Convert EthBlockVerbose to types.Block
		header := &types.Header{
			ParentHash: blockVerbose.ParentHash,
			Number:     big.NewInt(int64(blockVerbose.Number)),
			Time:       blockVerbose.Time,
		}
		block = types.NewBlockWithHeader(header).WithBody(types.Body{
			Transactions: blockVerbose.Transactions,
		})
	} else {
		// Full mode: fetch entire block with all transactions in one call
		ep.logger.WithFields(logrus.Fields{
			"network":      ep.network,
			"block_number": blockNumber,
		}).Debug("Using full block fetch mode")

		block, err = ep.client.BlockByNumber(ctx, big.NewInt(int64(blockNumber)))
		if err != nil {
			ominiClient, ominiLimiter := ep.getNextOminiClient()
			if ominiClient != nil && ominiLimiter != nil {
				ep.logger.WithFields(logrus.Fields{
					"network":      ep.network,
					"block_number": blockNumber,
					"error":        err,
				}).Warn("Regular RPC full block fetch failed, trying omini fallback")
				if limiterErr := ominiLimiter.Wait(ctx); limiterErr != nil {
					ep.logger.WithFields(logrus.Fields{
						"network":      ep.network,
						"block_number": blockNumber,
						"error":        limiterErr,
					}).Warn("Omini rate limiter error")
				} else {
					block, err = ominiClient.BlockByNumber(ctx, big.NewInt(int64(blockNumber)))
				}
			}
		}

		if err != nil {
			return nil, err
		}
	}

	// Store in cache with short TTL - blocks are only needed for immediate retry scenarios
	// Reduced from 30 minutes to 2 minutes to lower memory pressure
	if err := ep.storage.SetCache(ctx, cacheKey, block, 2*time.Minute); err != nil {
		ep.logger.WithFields(logrus.Fields{
			"network":      ep.network,
			"block_number": blockNumber,
			"error":        err,
		}).Warn("Failed to cache block")
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
