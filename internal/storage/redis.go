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

// RedisStorage implements Storage interface using Redis
type RedisStorage struct {
	client *redis.Client
	logger *logrus.Logger

	bloom *WalletBloom
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

	rs := &RedisStorage{
		client: client,
		logger: logger,
		bloom:  NewWalletBloom(logger),
	}

	// Seed bloom filters from current Redis state (best-effort)
	networks := []types.BlockchainType{types.Ethereum, types.BSC, types.Bitcoin, types.Tron}
	if err := rs.bloom.SeedAll(ctx, networks, rs.GetWatchedWallets); err != nil {
		logger.Warnf("Failed to initialize Bloom filters: %v", err)
	}

	return rs, nil
}

// Wallet tracking methods
func (r *RedisStorage) AddWatchedWallet(ctx context.Context, network types.BlockchainType, address, walletID string) error {
	key := fmt.Sprintf("watch:wallets:%s", network)
	if err := r.client.HSet(ctx, key, address, walletID).Err(); err != nil {
		return err
	}
	// Update Bloom filter (best-effort)
	r.bloom.Add(network, address)
	return nil
}

func (r *RedisStorage) RemoveWatchedWallet(ctx context.Context, network types.BlockchainType, address string) error {
	key := fmt.Sprintf("watch:wallets:%s", network)
	return r.client.HDel(ctx, key, address).Err()
}

func (r *RedisStorage) IsWatchedWallet(ctx context.Context, network types.BlockchainType, address string) (string, bool, error) {
	key := fmt.Sprintf("watch:wallets:%s", network)

	// Fast negative path via Bloom pre-check
	if r.bloom != nil && !r.bloom.MaybeContains(network, address) {
		return "", false, nil
	}

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

func (r *RedisStorage) ForceSetLastProcessedBlock(ctx context.Context, network types.BlockchainType, blockNumber uint64) error {
	key := "last_processed_blocks"
	field := string(network)
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
	windowSize := uint64(100000) // 100k blocks per window
	windowKey := fmt.Sprintf("processed_blocks:%s:%d", network, blockNumber/windowSize)
	bitPos := int64(blockNumber % windowSize)
	result, err := r.client.GetBit(ctx, windowKey, bitPos).Result()
	return result == 1, err
}

func (r *RedisStorage) MarkBlockProcessed(ctx context.Context, network types.BlockchainType, blockNumber uint64) error {
	windowSize := uint64(100000) // 100k blocks per window
	windowKey := fmt.Sprintf("processed_blocks:%s:%d", network, blockNumber/windowSize)
	bitPos := int64(blockNumber % windowSize)

	// Set the bit and add expiration to the window (30 days)
	err := r.client.SetBit(ctx, windowKey, bitPos, 1).Err()
	if err != nil {
		return err
	}

	if err := r.client.Expire(ctx, windowKey, 30*24*time.Hour).Err(); err != nil {
		return err
	}

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
