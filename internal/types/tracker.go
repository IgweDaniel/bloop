package types

// TrackerStats contains performance and health statistics
type TrackerStats struct {
	Network         BlockchainType `json:"network"`
	IsRunning       bool           `json:"is_running"`
	ProcessedBlocks uint64         `json:"processed_blocks"`
	ProcessedTxs    uint64         `json:"processed_txs"`
	WatchedWallets  int            `json:"watched_wallets"`
	LastBlockHeight uint64         `json:"last_block_height"`
	Uptime          string         `json:"uptime"`
	ErrorCount      uint64         `json:"error_count"`
}
