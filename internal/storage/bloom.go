package storage

import (
	"context"
	"sync"

	"github.com/bits-and-blooms/bloom/v3"
	"github.com/igwedaniel/bloop/internal/types"
	"github.com/sirupsen/logrus"
)

// WalletBloom manages per-network Bloom filters for watched wallets.
// It is append-only; removals require periodic refresh from source of truth.
type WalletBloom struct {
	mu          sync.RWMutex
	filters     map[types.BlockchainType]*bloom.BloomFilter
	logger      *logrus.Logger
	falsePos    float64
	minCapacity uint
}

// NewWalletBloom creates a new WalletBloom with sensible defaults.
func NewWalletBloom(logger *logrus.Logger) *WalletBloom {
	return &WalletBloom{
		filters:     make(map[types.BlockchainType]*bloom.BloomFilter),
		logger:      logger,
		falsePos:    0.001, // 0.1%
		minCapacity: 1000,
	}
}

// SeedAll seeds all provided networks using a getter function that returns address->walletID maps.
func (wb *WalletBloom) SeedAll(
	ctx context.Context,
	networks []types.BlockchainType,
	get func(ctx context.Context, network types.BlockchainType) (map[string]string, error),
) error {
	for _, n := range networks {
		if err := wb.Refresh(ctx, n, get); err != nil {
			return err
		}
	}
	return nil
}

// Refresh rebuilds the filter for a network from the source of truth.
func (wb *WalletBloom) Refresh(
	ctx context.Context,
	network types.BlockchainType,
	get func(ctx context.Context, network types.BlockchainType) (map[string]string, error),
) error {
	m, err := get(ctx, network)
	if err != nil {
		return err
	}

	estSize := uint(len(m))
	if estSize < wb.minCapacity {
		estSize = wb.minCapacity
	}
	bf := bloom.NewWithEstimates(estSize, wb.falsePos)
	for address := range m {
		bf.AddString(address)
	}

	wb.mu.Lock()
	wb.filters[network] = bf
	wb.mu.Unlock()
	return nil
}

// Add inserts an address into the network's filter, creating one if missing.
func (wb *WalletBloom) Add(network types.BlockchainType, address string) {
	wb.mu.RLock()
	bf := wb.filters[network]
	wb.mu.RUnlock()
	if bf == nil {
		wb.mu.Lock()
		if wb.filters[network] == nil {
			wb.filters[network] = bloom.NewWithEstimates(wb.minCapacity, wb.falsePos)
		}
		bf = wb.filters[network]
		wb.mu.Unlock()
	}
	bf.AddString(address)
}

// MaybeContains returns true if the filter suggests membership or filter absent.
func (wb *WalletBloom) MaybeContains(network types.BlockchainType, address string) bool {
	wb.mu.RLock()
	bf := wb.filters[network]
	wb.mu.RUnlock()
	if bf == nil {
		return true // no filter -> fall back to exact check
	}
	return bf.TestString(address)
}
