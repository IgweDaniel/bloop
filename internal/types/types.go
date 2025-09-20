package types

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// BlockchainType represents different blockchain networks
type BlockchainType string

const (
	Ethereum BlockchainType = "ETH"
	Bitcoin  BlockchainType = "BTC"
	BSC      BlockchainType = "BSC"
)

// Currency represents different cryptocurrencies
type Currency string

const (
	ETH  Currency = "ETH"
	BTC  Currency = "BTC"
	USDT Currency = "USDT"
)

// TransactionStatus represents the status of a transaction
type TransactionStatus string

const (
	StatusPending   TransactionStatus = "PENDING"
	StatusConfirmed TransactionStatus = "CONFIRMED"
	StatusFailed    TransactionStatus = "FAILED"
)

// WalletDeposit represents a detected deposit to a watched wallet
type WalletDeposit struct {
	TxHash        string            `json:"tx_hash"`
	WalletID      string            `json:"wallet_id"`
	WalletAddress string            `json:"wallet_address"`
	FromAddress   string            `json:"from_address"`
	Amount        string            `json:"amount"`
	Currency      Currency          `json:"currency"`
	Network       BlockchainType    `json:"network"`
	BlockNumber   uint64            `json:"block_number"`
	Confirmations uint64            `json:"confirmations"`
	Timestamp     time.Time         `json:"timestamp"`
	NetworkFee    string            `json:"network_fee"`
	Status        TransactionStatus `json:"status"`
	RawData       interface{}       `json:"raw_data,omitempty"`
}

// Block represents a blockchain block
type Block struct {
	Number       uint64    `json:"number"`
	Hash         string    `json:"hash"`
	Timestamp    time.Time `json:"timestamp"`
	Transactions []string  `json:"transactions"`
}

// Transaction represents a blockchain transaction
type Transaction struct {
	Hash        string          `json:"hash"`
	From        common.Address  `json:"from"`
	To          *common.Address `json:"to"`
	Value       *big.Int        `json:"value"`
	Gas         uint64          `json:"gas"`
	GasPrice    *big.Int        `json:"gas_price"`
	BlockNumber uint64          `json:"block_number"`
	Status      uint64          `json:"status"`
}

// WatchedWallet represents a wallet being monitored
type WatchedWallet struct {
	ID      string         `json:"id"`
	Address common.Address `json:"address"`
	Network BlockchainType `json:"network"`
}

// ProcessingProgress tracks block processing progress
type ProcessingProgress struct {
	BlockNumber     uint64    `json:"block_number"`
	TotalTxs        int       `json:"total_txs"`
	ProcessedTxs    int       `json:"processed_txs"`
	LastProcessedTx string    `json:"last_processed_tx"`
	StartedAt       time.Time `json:"started_at"`
	LastUpdatedAt   time.Time `json:"last_updated_at"`
}

// ProviderHealth tracks RPC provider health
type ProviderHealth struct {
	URL           string        `json:"url"`
	IsHealthy     bool          `json:"is_healthy"`
	LastError     string        `json:"last_error,omitempty"`
	FailureCount  int           `json:"failure_count"`
	LastCheckedAt time.Time     `json:"last_checked_at"`
	ResponseTime  time.Duration `json:"response_time"`
}

// CircuitBreakerState represents circuit breaker states
type CircuitBreakerState string

const (
	CircuitClosed   CircuitBreakerState = "CLOSED"
	CircuitOpen     CircuitBreakerState = "OPEN"
	CircuitHalfOpen CircuitBreakerState = "HALF_OPEN"
)

// Event represents a message to be published
type Event struct {
	Type      string      `json:"type"`
	Payload   interface{} `json:"payload"`
	Timestamp time.Time   `json:"timestamp"`
	Source    string      `json:"source"`
}

// EventType constants
const (
	EventTypeWalletDeposit  = "wallet.deposit"
	EventTypeBlockProcessed = "block.processed"
	EventTypeProviderHealth = "provider.health"
)
