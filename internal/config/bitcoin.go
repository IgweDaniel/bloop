package config

import "time"

// BitcoinConfig contains Bitcoin-specific configuration
type BitcoinConfig struct {
	RPCURL              string        `mapstructure:"rpc_url"`
	Username            string        `mapstructure:"username"`
	Password            string        `mapstructure:"password"`
	Confirmations       int           `mapstructure:"confirmations"`
	BatchSize           int           `mapstructure:"batch_size"`
	MaxConcurrentBlocks int           `mapstructure:"max_concurrent_blocks"`
	RPCTimeout          time.Duration `mapstructure:"rpc_timeout"`
	RetryAttempts       int           `mapstructure:"retry_attempts"`
	RetryDelay          time.Duration `mapstructure:"retry_delay"`
}

// TODO: Add BitcoinConfig to main Config struct when implementing Bitcoin support
