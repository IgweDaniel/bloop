package types

// TrackerStats contains performance and health statistics
type TrackerStats struct {
	Network             BlockchainType    `json:"network"`
	IsRunning           bool              `json:"is_running"`
	ProcessedBlocks     uint64            `json:"processed_blocks"`
	ProcessedTxs        uint64            `json:"processed_txs"`
	WatchedWallets      int               `json:"watched_wallets"`
	LastBlockHeight     uint64            `json:"last_block_height"`
	CurrentBlockHeight  uint64            `json:"current_block_height"`
	SafeHead            uint64            `json:"safe_head"`
	BlockGap            uint64            `json:"block_gap"`
	Confirmations       int               `json:"confirmations"`
	BlockQueueLen       int               `json:"block_queue_len"`
	BlockQueueCap       int               `json:"block_queue_cap"`
	LastEnqueuedBlock   uint64            `json:"last_enqueued_block"`
	LastDequeuedBlock   uint64            `json:"last_dequeued_block"`
	LastProcessingBlock uint64            `json:"last_processing_block"`
	SkippedChannelFull  uint64            `json:"skipped_channel_full"`
	SkippedProcessed    uint64            `json:"skipped_processed"`
	BlockGapConsecutive uint64            `json:"block_gap_consecutive"`
	InFlightTxs         uint64            `json:"in_flight_txs"`
	APIProviders        map[string]uint64 `json:"api_providers,omitempty"`
	APIProviderErrors   map[string]uint64 `json:"api_provider_errors,omitempty"`
	APIProviderLast     string            `json:"api_provider_last,omitempty"`
	Uptime              string            `json:"uptime"`
	ErrorCount          uint64            `json:"error_count"`
}
