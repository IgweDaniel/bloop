package ethereum

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/igwedaniel/bloop/internal/config"
	"github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
)

// CircuitBreakerState represents the state of a circuit breaker
type CircuitBreakerState int

const (
	CircuitClosed CircuitBreakerState = iota
	CircuitOpen
	CircuitHalfOpen
)

type ProviderStatus struct {
	URL           string
	State         CircuitBreakerState
	Failures      int
	LastFailure   time.Time
	LastSuccess   time.Time
	ResponseTimes []time.Duration
	mu            sync.RWMutex
}

type EthereumClient struct {
	providers    []*ethclient.Client
	rpcClients   []*rpc.Client
	statuses     []*ProviderStatus
	rateLimiter  *rate.Limiter
	config       *config.EthereumConfig
	logger       *logrus.Logger
	currentIndex int
	mu           sync.RWMutex
}

func NewEthereumClient(cfg *config.EthereumConfig, logger *logrus.Logger) (*EthereumClient, error) {
	if len(cfg.RPCURLs) == 0 {
		return nil, fmt.Errorf("no RPC URLs provided")
	}

	client := &EthereumClient{
		providers:   make([]*ethclient.Client, 0, len(cfg.RPCURLs)),
		rpcClients:  make([]*rpc.Client, 0, len(cfg.RPCURLs)),
		statuses:    make([]*ProviderStatus, 0, len(cfg.RPCURLs)),
		rateLimiter: rate.NewLimiter(rate.Every(100*time.Millisecond), 10), // 10 requests per second
		config:      cfg,
		logger:      logger,
	}

	for _, url := range cfg.RPCURLs {
		if err := client.addProvider(url); err != nil {
			logger.Warnf("Failed to add provider %s: %v", url, err)
			continue
		}
	}

	if len(client.providers) == 0 {
		return nil, fmt.Errorf("no working RPC providers available")
	}

	go client.healthCheckLoop()

	return client, nil
}

func (c *EthereumClient) addProvider(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.config.RPCTimeout)
	defer cancel()

	rpcClient, err := rpc.DialContext(ctx, url)
	if err != nil {
		return fmt.Errorf("failed to dial RPC: %w", err)
	}

	ethClient := ethclient.NewClient(rpcClient)

	_, err = ethClient.BlockNumber(ctx)
	if err != nil {
		rpcClient.Close()
		return fmt.Errorf("failed to test connection: %w", err)
	}

	status := &ProviderStatus{
		URL:           url,
		State:         CircuitClosed,
		LastSuccess:   time.Now(),
		ResponseTimes: make([]time.Duration, 0, 10),
	}

	c.providers = append(c.providers, ethClient)
	c.rpcClients = append(c.rpcClients, rpcClient)
	c.statuses = append(c.statuses, status)

	c.logger.Infof("Added RPC provider: %s", url)
	return nil
}

// getHealthyProvider returns the next healthy provider using round-robin
func (c *EthereumClient) getHealthyProvider() (*ethclient.Client, *ProviderStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	startIndex := c.currentIndex
	for i := 0; i < len(c.providers); i++ {
		index := (startIndex + i) % len(c.providers)
		status := c.statuses[index]

		status.mu.RLock()
		isHealthy := status.State == CircuitClosed ||
			(status.State == CircuitHalfOpen && time.Since(status.LastFailure) > 30*time.Second)
		status.mu.RUnlock()

		if isHealthy {
			c.currentIndex = (index + 1) % len(c.providers)
			return c.providers[index], status, nil
		}
	}

	return nil, nil, fmt.Errorf("no healthy providers available")
}

// executeWithRetry executes a function with retry logic and circuit breaker
func (c *EthereumClient) executeWithRetry(ctx context.Context, operation func(*ethclient.Client) error) error {
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limit exceeded: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < c.config.RetryAttempts; attempt++ {
		provider, status, err := c.getHealthyProvider()
		if err != nil {
			return err
		}

		start := time.Now()
		err = operation(provider)
		duration := time.Since(start)

		// Check if this is a transaction type error - don't mark provider as unhealthy
		if err != nil && strings.Contains(err.Error(), "transaction type not supported") {
			c.logger.Debugf("Provider %s doesn't support transaction type, trying next provider", status.URL)
			// Don't update provider status as unhealthy for transaction type errors
			lastErr = err
			continue
		}

		c.updateProviderStatus(status, err, duration)

		if err == nil {
			return nil
		}

		lastErr = err
		if attempt < c.config.RetryAttempts-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.config.RetryDelay):
			}
		}
	}

	return fmt.Errorf("all retry attempts failed: %w", lastErr)
}

func (c *EthereumClient) updateProviderStatus(status *ProviderStatus, err error, duration time.Duration) {
	status.mu.Lock()
	defer status.mu.Unlock()

	status.ResponseTimes = append(status.ResponseTimes, duration)
	if len(status.ResponseTimes) > 10 {
		status.ResponseTimes = status.ResponseTimes[1:]
	}

	if err != nil {
		status.Failures++
		status.LastFailure = time.Now()

		// Open circuit breaker after 3 failures
		if status.Failures >= 3 && status.State == CircuitClosed {
			status.State = CircuitOpen
			c.logger.Warnf("Circuit breaker opened for provider %s after %d failures", status.URL, status.Failures)
		}
	} else {
		status.Failures = 0
		status.LastSuccess = time.Now()

		// Close circuit breaker on successful request
		if status.State == CircuitHalfOpen {
			status.State = CircuitClosed
			c.logger.Infof("Circuit breaker closed for provider %s", status.URL)
		}
	}
}

func (c *EthereumClient) healthCheckLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		c.performHealthCheck()
	}
}

func (c *EthereumClient) performHealthCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for i, provider := range c.providers {
		status := c.statuses[i]

		status.mu.RLock()
		shouldCheck := status.State == CircuitOpen && time.Since(status.LastFailure) > 60*time.Second
		status.mu.RUnlock()

		if shouldCheck {
			start := time.Now()
			_, err := provider.BlockNumber(ctx)
			duration := time.Since(start)

			if err == nil {
				status.mu.Lock()
				status.State = CircuitHalfOpen
				status.Failures = 0
				status.LastSuccess = time.Now()
				status.mu.Unlock()
				c.logger.Infof("Provider %s is recovering", status.URL)
			} else {
				c.updateProviderStatus(status, err, duration)
			}
		}
	}
}

func (c *EthereumClient) BlockNumber(ctx context.Context) (uint64, error) {
	var blockNumber uint64
	err := c.executeWithRetry(ctx, func(client *ethclient.Client) error {
		var err error
		blockNumber, err = client.BlockNumber(ctx)
		return err
	})
	return blockNumber, err
}

func (c *EthereumClient) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	var header *types.Header
	err := c.executeWithRetry(ctx, func(client *ethclient.Client) error {
		var err error
		header, err = client.HeaderByNumber(ctx, number)
		return err
	})
	return header, err
}

func (c *EthereumClient) BlockByNumber(ctx context.Context, number *big.Int) (*types.Block, error) {
	var block *types.Block
	err := c.executeWithRetry(ctx, func(client *ethclient.Client) error {
		var err error
		block, err = client.BlockByNumber(ctx, number)
		return err
	})
	return block, err
}

// TransactionByHash fetches a single transaction by hash
func (c *EthereumClient) TransactionByHash(ctx context.Context, hash common.Hash) (*types.Transaction, bool, error) {
	var tx *types.Transaction
	var isPending bool
	err := c.executeWithRetry(ctx, func(client *ethclient.Client) error {
		var err error
		tx, isPending, err = client.TransactionByHash(ctx, hash)
		return err
	})
	return tx, isPending, err
}

func (c *EthereumClient) TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error) {
	var receipt *types.Receipt
	err := c.executeWithRetry(ctx, func(client *ethclient.Client) error {
		var err error
		receipt, err = client.TransactionReceipt(ctx, txHash)
		return err
	})
	return receipt, err
}

func (c *EthereumClient) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	var logs []types.Log
	err := c.executeWithRetry(ctx, func(client *ethclient.Client) error {
		var err error
		logs, err = client.FilterLogs(ctx, q)
		return err
	})
	return logs, err
}

func (c *EthereumClient) SubscribeNewHead(ctx context.Context, ch chan<- *types.Header) (ethereum.Subscription, error) {
	provider, _, err := c.getHealthyProvider()
	if err != nil {
		return nil, err
	}
	return provider.SubscribeNewHead(ctx, ch)
}

// BatchCall performs multiple RPC calls in a single batch
func (c *EthereumClient) BatchCall(ctx context.Context, calls []rpc.BatchElem) error {
	return c.executeWithRetry(ctx, func(client *ethclient.Client) error {
		provider, _, err := c.getHealthyProvider()
		if err != nil {
			return err
		}

		// Find the corresponding RPC client
		var rpcClient *rpc.Client
		for i, p := range c.providers {
			if p == provider {
				rpcClient = c.rpcClients[i]
				break
			}
		}

		if rpcClient == nil {
			return fmt.Errorf("could not find RPC client for provider")
		}

		return rpcClient.BatchCallContext(ctx, calls)
	})
}

func (c *EthereumClient) Close() {
	for _, client := range c.rpcClients {
		client.Close()
	}
}

// GetProviderStatuses returns the current status of all providers
func (c *EthereumClient) GetProviderStatuses() []*ProviderStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	statuses := make([]*ProviderStatus, len(c.statuses))
	for i, status := range c.statuses {
		status.mu.RLock()
		statuses[i] = &ProviderStatus{
			URL:         status.URL,
			State:       status.State,
			Failures:    status.Failures,
			LastFailure: status.LastFailure,
			LastSuccess: status.LastSuccess,
		}
		status.mu.RUnlock()
	}
	return statuses
}

// EthBlockVerbose represents a block with full transaction details
type EthBlockVerbose struct {
	Hash         common.Hash
	Number       uint64
	Time         uint64
	ParentHash   common.Hash
	Transactions []*types.Transaction
}

// GetBlockVerbose fetches a block with all transaction details by fetching each transaction individually.
// This is more efficient for free RPC endpoints with rotation as it distributes the load across providers
// and avoids expensive single calls to fetch full blocks with transaction bodies.
// Similar to Bitcoin's GetBlockVerbose, it uses concurrency control and rate limiting.
func (c *EthereumClient) GetBlockVerbose(ctx context.Context, blockNumber uint64) (*EthBlockVerbose, error) {
	// First, get the block header (lightweight call)
	header, err := c.HeaderByNumber(ctx, big.NewInt(int64(blockNumber)))
	if err != nil {
		return nil, fmt.Errorf("failed to get block header: %w", err)
	}

	// Get transaction hashes (lightweight - only fetches hashes, not full tx data)
	// We use eth_getBlockByNumber with transactions=false to get only tx hashes
	var blockData struct {
		Hash         common.Hash   `json:"hash"`
		Number       string        `json:"number"`
		Timestamp    string        `json:"timestamp"`
		ParentHash   common.Hash   `json:"parentHash"`
		Transactions []common.Hash `json:"transactions"`
	}

	provider, _, err := c.getHealthyProvider()
	if err != nil {
		return nil, err
	}

	// Find the corresponding RPC client
	var rpcClient *rpc.Client
	for i, p := range c.providers {
		if p == provider {
			rpcClient = c.rpcClients[i]
			break
		}
	}

	if rpcClient == nil {
		return nil, fmt.Errorf("could not find RPC client for provider")
	}

	// Fetch block with transaction hashes only (not full transaction data)
	if err := rpcClient.CallContext(ctx, &blockData, "eth_getBlockByNumber", fmt.Sprintf("0x%x", blockNumber), false); err != nil {
		return nil, fmt.Errorf("failed to get block transaction hashes: %w", err)
	}

	txHashes := blockData.Transactions
	if len(txHashes) == 0 {
		// Empty block
		return &EthBlockVerbose{
			Hash:         header.Hash(),
			Number:       header.Number.Uint64(),
			Time:         header.Time,
			ParentHash:   header.ParentHash,
			Transactions: []*types.Transaction{},
		}, nil
	}

	// Fetch each transaction concurrently with rate limiting (similar to Bitcoin implementation)
	fetchConcurrency := 20 // Can be made configurable
	if c.config.TxFetchConcurrency > 0 {
		fetchConcurrency = c.config.TxFetchConcurrency
	}

	results := make([]*types.Transaction, len(txHashes))
	type txResult struct {
		idx int
		tx  *types.Transaction
		err error
	}

	sem := make(chan struct{}, fetchConcurrency)
	wg := sync.WaitGroup{}
	resCh := make(chan txResult, len(txHashes))

	for i, txHash := range txHashes {
		wg.Add(1)
		go func(i int, txHash common.Hash) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Rate limiting
			if err := c.rateLimiter.Wait(ctx); err != nil {
				resCh <- txResult{idx: i, err: err}
				return
			}

			// Fetch transaction using the rotation client
			tx, _, err := c.TransactionByHash(ctx, txHash)
			if err != nil {
				resCh <- txResult{idx: i, err: fmt.Errorf("failed to fetch tx %s: %w", txHash.Hex(), err)}
				return
			}

			resCh <- txResult{idx: i, tx: tx}
		}(i, txHash)
	}

	wg.Wait()
	close(resCh)

	for r := range resCh {
		if r.err != nil {
			return nil, r.err
		}
		results[r.idx] = r.tx
	}

	return &EthBlockVerbose{
		Hash:         header.Hash(),
		Number:       header.Number.Uint64(),
		Time:         header.Time,
		ParentHash:   header.ParentHash,
		Transactions: results,
	}, nil
}
