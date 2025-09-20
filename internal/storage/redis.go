package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/igwedaniel/bloop/internal/config"
	"github.com/igwedaniel/bloop/internal/types"
	"github.com/sirupsen/logrus"
)

// Storage interface for loose coupling
type Storage interface {
	// Wallet tracking
	AddWatchedWallet(ctx context.Context, network types.BlockchainType, address, walletID string) error
	RemoveWatchedWallet(ctx context.Context, network types.BlockchainType, address string) error
	IsWatchedWallet(ctx context.Context, network types.BlockchainType, address string) (string, bool, error)
	GetWatchedWallets(ctx context.Context, network types.BlockchainType) (map[string]string, error)

	// Block processing
	SetLastProcessedBlock(ctx context.Context, network types.BlockchainType, blockNumber uint64) error
	GetLastProcessedBlock(ctx context.Context, network types.BlockchainType) (uint64, error)
	IsBlockProcessed(ctx context.Context, network types.BlockchainType, blockNumber uint64) (bool, error)
	MarkBlockProcessed(ctx context.Context, network types.BlockchainType, blockNumber uint64) error
	// AdvanceHighWaterMark tries to advance lastProcessed forward while contiguous bits are set
	AdvanceHighWaterMark(ctx context.Context, network types.BlockchainType) error

	// Transaction processing progress
	AddProcessedTransaction(ctx context.Context, network types.BlockchainType, blockNumber uint64, txHash string) error
	GetProcessedTransactions(ctx context.Context, network types.BlockchainType, blockNumber uint64) ([]string, error)
	ClearBlockProgress(ctx context.Context, network types.BlockchainType, blockNumber uint64) error

	// Caching
	SetCache(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	GetCache(ctx context.Context, key string, dest interface{}) error
	DeleteCache(ctx context.Context, key string) error

	// Health check
	Ping(ctx context.Context) error
	Close() error
}

// RedisStorage implements Storage interface using Redis
type RedisStorage struct {
	client *redis.Client
	logger *logrus.Logger
}

// NewRedisStorage creates a new Redis storage instance
func NewRedisStorage(cfg *config.RedisConfig, logger *logrus.Logger) (*RedisStorage, error) {
	opt, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Redis URL: %w", err)
	}

	opt.PoolSize = cfg.PoolSize
	opt.MinIdleConns = cfg.MinIdleConns
	opt.DialTimeout = cfg.DialTimeout
	opt.ReadTimeout = cfg.ReadTimeout
	opt.WriteTimeout = cfg.WriteTimeout

	client := redis.NewClient(opt)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &RedisStorage{
		client: client,
		logger: logger,
	}, nil
}

// Wallet tracking methods
func (r *RedisStorage) AddWatchedWallet(ctx context.Context, network types.BlockchainType, address, walletID string) error {
	key := fmt.Sprintf("watch:wallets:%s", network)
	return r.client.HSet(ctx, key, address, walletID).Err()
}

func (r *RedisStorage) RemoveWatchedWallet(ctx context.Context, network types.BlockchainType, address string) error {
	key := fmt.Sprintf("watch:wallets:%s", network)
	return r.client.HDel(ctx, key, address).Err()
}

func (r *RedisStorage) IsWatchedWallet(ctx context.Context, network types.BlockchainType, address string) (string, bool, error) {
	key := fmt.Sprintf("watch:wallets:%s", network)
	walletID, err := r.client.HGet(ctx, key, address).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return walletID, true, nil
}

func (r *RedisStorage) GetWatchedWallets(ctx context.Context, network types.BlockchainType) (map[string]string, error) {
	key := fmt.Sprintf("watch:wallets:%s", network)
	return r.client.HGetAll(ctx, key).Result()
}

// Block processing methods
func (r *RedisStorage) SetLastProcessedBlock(ctx context.Context, network types.BlockchainType, blockNumber uint64) error {
	// Monotonic update without Lua (best effort): only set if new >= existing
	key := "last_processed_blocks"
	field := string(network)

	curStr, err := r.client.HGet(ctx, key, field).Result()
	if err != nil && err != redis.Nil {
		return err
	}
	if err == nil { // existing value present
		if cur, parseErr := strconv.ParseUint(curStr, 10, 64); parseErr == nil {
			if blockNumber < cur {
				// Do not move backwards
				return nil
			}
		}
	}
	return r.client.HSet(ctx, key, field, blockNumber).Err()
}

func (r *RedisStorage) GetLastProcessedBlock(ctx context.Context, network types.BlockchainType) (uint64, error) {
	key := "last_processed_blocks"
	result, err := r.client.HGet(ctx, key, string(network)).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(result, 10, 64)
}

func (r *RedisStorage) IsBlockProcessed(ctx context.Context, network types.BlockchainType, blockNumber uint64) (bool, error) {
	// Use a time-based window approach instead of a broken bitmap
	windowSize := uint64(100000) // 100k blocks per window
	windowKey := fmt.Sprintf("processed_blocks:%s:%d", network, blockNumber/windowSize)
	bitPos := int64(blockNumber % windowSize)
	result, err := r.client.GetBit(ctx, windowKey, bitPos).Result()
	return result == 1, err
}

func (r *RedisStorage) MarkBlockProcessed(ctx context.Context, network types.BlockchainType, blockNumber uint64) error {
	// Use a time-based window approach instead of a broken bitmap
	windowSize := uint64(100000) // 100k blocks per window
	windowKey := fmt.Sprintf("processed_blocks:%s:%d", network, blockNumber/windowSize)
	bitPos := int64(blockNumber % windowSize)

	// Set the bit and add expiration to the window (30 days)
	err := r.client.SetBit(ctx, windowKey, bitPos, 1).Err()
	if err != nil {
		return err
	}

	// Set expiration on the window key (30 days)
	if err := r.client.Expire(ctx, windowKey, 30*24*time.Hour).Err(); err != nil {
		return err
	}

	// HWM advancement handled in tracker (needs confirmations/current height context)
	return nil
}

// AdvanceHighWaterMark advances lastProcessed while next contiguous bits are set
func (r *RedisStorage) AdvanceHighWaterMark(ctx context.Context, network types.BlockchainType) error {
	// Read current lastProcessed
	last, err := r.GetLastProcessedBlock(ctx, network)
	if err != nil {
		return err
	}

	// Walk forward while contiguous blocks are marked processed
	const windowSize = uint64(100000)
	advanced := last
	for {
		next := advanced + 1
		windowKey := fmt.Sprintf("processed_blocks:%s:%d", network, next/windowSize)
		bitPos := int64(next % windowSize)
		v, err := r.client.GetBit(ctx, windowKey, bitPos).Result()
		if err != nil {
			return err
		}
		if v != 1 {
			break
		}
		advanced = next
		// Small guard to avoid pathological long loops; advance in batches is fine
		// but typically this moves a few steps only
		if advanced-last > 10000 {
			break
		}
	}

	if advanced > last {
		return r.SetLastProcessedBlock(ctx, network, advanced)
	}
	return nil
}

// Transaction processing progress methods
func (r *RedisStorage) AddProcessedTransaction(ctx context.Context, network types.BlockchainType, blockNumber uint64, txHash string) error {
	key := fmt.Sprintf("%s:block_progress:%d", network, blockNumber)
	return r.client.SAdd(ctx, key, txHash).Err()
}

func (r *RedisStorage) GetProcessedTransactions(ctx context.Context, network types.BlockchainType, blockNumber uint64) ([]string, error) {
	key := fmt.Sprintf("%s:block_progress:%d", network, blockNumber)
	return r.client.SMembers(ctx, key).Result()
}

func (r *RedisStorage) ClearBlockProgress(ctx context.Context, network types.BlockchainType, blockNumber uint64) error {
	key := fmt.Sprintf("%s:block_progress:%d", network, blockNumber)
	return r.client.Del(ctx, key).Err()
}

// Caching methods
func (r *RedisStorage) SetCache(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to marshal cache value: %w", err)
	}
	return r.client.Set(ctx, key, data, ttl).Err()
}

func (r *RedisStorage) GetCache(ctx context.Context, key string, dest interface{}) error {
	data, err := r.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return fmt.Errorf("cache key not found: %s", key)
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dest)
}

func (r *RedisStorage) DeleteCache(ctx context.Context, key string) error {
	return r.client.Del(ctx, key).Err()
}

// Health check methods
func (r *RedisStorage) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

func (r *RedisStorage) Close() error {
	return r.client.Close()
}

// InMemoryStorage is a simple in-memory implementation for testing
type InMemoryStorage struct {
	watchedWallets      map[string]map[string]string   // network -> address -> walletID
	lastProcessedBlocks map[string]uint64              // network -> blockNumber
	processedBlocks     map[string]map[uint64]bool     // network -> blockNumber -> processed
	blockProgress       map[string]map[uint64][]string // network -> blockNumber -> txHashes
	cache               map[string]interface{}
}

func NewInMemoryStorage() *InMemoryStorage {
	return &InMemoryStorage{
		watchedWallets:      make(map[string]map[string]string),
		lastProcessedBlocks: make(map[string]uint64),
		processedBlocks:     make(map[string]map[uint64]bool),
		blockProgress:       make(map[string]map[uint64][]string),
		cache:               make(map[string]interface{}),
	}
}

// Implement all Storage interface methods for InMemoryStorage
// (Implementation omitted for brevity, but would follow similar patterns)

func (m *InMemoryStorage) AddWatchedWallet(ctx context.Context, network types.BlockchainType, address, walletID string) error {
	if m.watchedWallets[string(network)] == nil {
		m.watchedWallets[string(network)] = make(map[string]string)
	}
	m.watchedWallets[string(network)][address] = walletID
	return nil
}

func (m *InMemoryStorage) IsWatchedWallet(ctx context.Context, network types.BlockchainType, address string) (string, bool, error) {
	if wallets, exists := m.watchedWallets[string(network)]; exists {
		if walletID, found := wallets[address]; found {
			return walletID, true, nil
		}
	}
	return "", false, nil
}

func (m *InMemoryStorage) Ping(ctx context.Context) error {
	return nil
}

func (m *InMemoryStorage) Close() error {
	return nil
}

// Add other required methods...
func (m *InMemoryStorage) RemoveWatchedWallet(ctx context.Context, network types.BlockchainType, address string) error {
	if wallets, exists := m.watchedWallets[string(network)]; exists {
		delete(wallets, address)
	}
	return nil
}

func (m *InMemoryStorage) GetWatchedWallets(ctx context.Context, network types.BlockchainType) (map[string]string, error) {
	if wallets, exists := m.watchedWallets[string(network)]; exists {
		return wallets, nil
	}
	return make(map[string]string), nil
}

func (m *InMemoryStorage) SetLastProcessedBlock(ctx context.Context, network types.BlockchainType, blockNumber uint64) error {
	// Monotonic: only move forward
	n := string(network)
	if cur, ok := m.lastProcessedBlocks[n]; ok {
		if blockNumber >= cur {
			m.lastProcessedBlocks[n] = blockNumber
		}
		return nil
	}
	m.lastProcessedBlocks[n] = blockNumber
	return nil
}

func (m *InMemoryStorage) GetLastProcessedBlock(ctx context.Context, network types.BlockchainType) (uint64, error) {
	if block, exists := m.lastProcessedBlocks[string(network)]; exists {
		return block, nil
	}
	return 0, nil
}

func (m *InMemoryStorage) IsBlockProcessed(ctx context.Context, network types.BlockchainType, blockNumber uint64) (bool, error) {
	if blocks, exists := m.processedBlocks[string(network)]; exists {
		return blocks[blockNumber], nil
	}
	return false, nil
}

func (m *InMemoryStorage) MarkBlockProcessed(ctx context.Context, network types.BlockchainType, blockNumber uint64) error {
	if m.processedBlocks[string(network)] == nil {
		m.processedBlocks[string(network)] = make(map[uint64]bool)
	}
	m.processedBlocks[string(network)][blockNumber] = true
	return m.AdvanceHighWaterMark(ctx, network)
}

func (m *InMemoryStorage) AdvanceHighWaterMark(ctx context.Context, network types.BlockchainType) error {
	n := string(network)
	last := m.lastProcessedBlocks[n]
	advanced := last
	for {
		next := advanced + 1
		if done := m.processedBlocks[n][next]; !done {
			break
		}
		advanced = next
		if advanced-last > 10000 {
			break
		}
	}
	if advanced > last {
		m.lastProcessedBlocks[n] = advanced
	}
	return nil
}

func (m *InMemoryStorage) AddProcessedTransaction(ctx context.Context, network types.BlockchainType, blockNumber uint64, txHash string) error {
	if m.blockProgress[string(network)] == nil {
		m.blockProgress[string(network)] = make(map[uint64][]string)
	}
	m.blockProgress[string(network)][blockNumber] = append(m.blockProgress[string(network)][blockNumber], txHash)
	return nil
}

func (m *InMemoryStorage) GetProcessedTransactions(ctx context.Context, network types.BlockchainType, blockNumber uint64) ([]string, error) {
	if progress, exists := m.blockProgress[string(network)]; exists {
		if txs, found := progress[blockNumber]; found {
			return txs, nil
		}
	}
	return []string{}, nil
}

func (m *InMemoryStorage) ClearBlockProgress(ctx context.Context, network types.BlockchainType, blockNumber uint64) error {
	if progress, exists := m.blockProgress[string(network)]; exists {
		delete(progress, blockNumber)
	}
	return nil
}

func (m *InMemoryStorage) SetCache(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	m.cache[key] = value
	return nil
}

func (m *InMemoryStorage) GetCache(ctx context.Context, key string, dest interface{}) error {
	if value, exists := m.cache[key]; exists {
		// Simple assignment - in real implementation, would need proper type handling
		*dest.(*interface{}) = value
		return nil
	}
	return fmt.Errorf("cache key not found: %s", key)
}

func (m *InMemoryStorage) DeleteCache(ctx context.Context, key string) error {
	delete(m.cache, key)
	return nil
}
