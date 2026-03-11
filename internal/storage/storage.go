package storage

import (
	"context"
	"time"

	"github.com/igwedaniel/bloop/internal/types"
)

type Storage interface {
	AddWatchedWallet(ctx context.Context, network types.BlockchainType, address, walletID string) error
	RemoveWatchedWallet(ctx context.Context, network types.BlockchainType, address string) error
	IsWatchedWallet(ctx context.Context, network types.BlockchainType, address string) (string, bool, error)
	GetWatchedWallets(ctx context.Context, network types.BlockchainType) (map[string]string, error)

	SetLastProcessedBlock(ctx context.Context, network types.BlockchainType, blockNumber uint64) error
	// ForceSetLastProcessedBlock sets the last processed block even if it moves backwards.
	ForceSetLastProcessedBlock(ctx context.Context, network types.BlockchainType, blockNumber uint64) error
	GetLastProcessedBlock(ctx context.Context, network types.BlockchainType) (uint64, error)
	IsBlockProcessed(ctx context.Context, network types.BlockchainType, blockNumber uint64) (bool, error)
	MarkBlockProcessed(ctx context.Context, network types.BlockchainType, blockNumber uint64) error
	// AdvanceHighWaterMark tries to advance lastProcessed forward while contiguous bits are set
	AdvanceHighWaterMark(ctx context.Context, network types.BlockchainType) error

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
