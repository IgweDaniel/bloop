package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/igwedaniel/bloop/internal/types"
	"github.com/spf13/viper"
)

type Config struct {
	Server     ServerConfig     `mapstructure:"server"`
	Redis      RedisConfig      `mapstructure:"redis"`
	RabbitMQ   RabbitMQConfig   `mapstructure:"rabbitmq"`
	EVM        []EVMConfig      `mapstructure:"evm"`
	UTXO       []UTXOConfig     `mapstructure:"utxo"`
	Tron       TronConfig       `mapstructure:"tron"`
	Monitoring MonitoringConfig `mapstructure:"monitoring"`
	Logging    LoggingConfig    `mapstructure:"logging"`
}

type ServerConfig struct {
	Port         int           `mapstructure:"port"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
}

type RedisConfig struct {
	URL          string        `mapstructure:"url"`
	PoolSize     int           `mapstructure:"pool_size"`
	MinIdleConns int           `mapstructure:"min_idle_conns"`
	DialTimeout  time.Duration `mapstructure:"dial_timeout"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
}

type RabbitMQConfig struct {
	URL           string `mapstructure:"url"`
	Exchange      string `mapstructure:"exchange"`
	QueuePrefix   string `mapstructure:"queue_prefix"`
	PrefetchCount int    `mapstructure:"prefetch_count"`
}

type EVMConfig struct {
	Network                types.BlockchainType `mapstructure:"network"`
	NativeCurrency         types.Currency       `mapstructure:"native_currency"`
	RPCURLs                []string             `mapstructure:"rpc_urls"`
	WSURL                  string               `mapstructure:"ws_url"`
	OminiRPCURL            string               `mapstructure:"omini_rpc_url"`             // Deprecated: use OminiRPCURLs
	OminiRPCURLs           []string             `mapstructure:"omini_rpc_urls"`            // Multiple omini RPC URLs for rotation
	OminiRequestsPerSecond int                  `mapstructure:"omini_requests_per_second"` // Rate limit for omini clients
	OminiRequestsBurst     int                  `mapstructure:"omini_requests_burst"`      // Burst capacity for omini clients
	Tokens                 []TokenConfig        `mapstructure:"tokens"`
	Confirmations          int                  `mapstructure:"confirmations"`
	BatchSize              int                  `mapstructure:"batch_size"`
	MaxConcurrentBlocks    int                  `mapstructure:"max_concurrent_blocks"`
	RPCTimeout             time.Duration        `mapstructure:"rpc_timeout"`
	RetryAttempts          int                  `mapstructure:"retry_attempts"`
	RetryDelay             time.Duration        `mapstructure:"retry_delay"`
	BlockFetchMode         string               `mapstructure:"block_fetch_mode"`     // "light" or "full" - light uses GetBlockVerbose with RPC rotation
	TxFetchConcurrency     int                  `mapstructure:"tx_fetch_concurrency"` // Concurrency for fetching individual transactions in light mode
	IsActive               bool                 `mapstructure:"is_active"`
	SkipNative             bool                 `mapstructure:"skip_native"` // if true, skip checking native; if false (zero value), check native
}

type TokenConfig struct {
	Currency string `mapstructure:"currency"`
	Contract string `mapstructure:"contract"`
	Decimals int32  `mapstructure:"decimals"`
}

// UTXOConfig contains configuration for Bitcoin-like UTXO chains.
type UTXOConfig struct {
	Network             types.BlockchainType `mapstructure:"network"`
	NativeCurrency      types.Currency       `mapstructure:"native_currency"`
	APIURL              string               `mapstructure:"api_url"`
	APIURLs             []string             `mapstructure:"api_urls"`
	WSURL               string               `mapstructure:"ws_url"`
	Confirmations       int                  `mapstructure:"confirmations"`
	BatchSize           int                  `mapstructure:"batch_size"`
	MaxConcurrentBlocks int                  `mapstructure:"max_concurrent_blocks"`
	RPCTimeout          time.Duration        `mapstructure:"rpc_timeout"`
	RetryAttempts       int                  `mapstructure:"retry_attempts"`
	RetryDelay          time.Duration        `mapstructure:"retry_delay"`
	TxFetchConcurrency  int                  `mapstructure:"tx_fetch_concurrency"`
	RequeueDelay        time.Duration        `mapstructure:"requeue_delay"`
	RequestsPerSecond   int                  `mapstructure:"requests_per_second"`
	RequestsBurst       int                  `mapstructure:"requests_burst"`
	IsActive            bool                 `mapstructure:"is_active"`
}

// TronConfig contains TRON-specific configuration.
type TronConfig struct {
	APIURL              string        `mapstructure:"api_url"`
	APIURLs             []string      `mapstructure:"api_urls"`
	APIKey              string        `mapstructure:"api_key"`
	Tokens              []TokenConfig `mapstructure:"tokens"`
	Confirmations       int           `mapstructure:"confirmations"`
	BatchSize           int           `mapstructure:"batch_size"`
	MaxConcurrentBlocks int           `mapstructure:"max_concurrent_blocks"`
	RPCTimeout          time.Duration `mapstructure:"rpc_timeout"`
	RetryAttempts       int           `mapstructure:"retry_attempts"`
	RetryDelay          time.Duration `mapstructure:"retry_delay"`
	RequeueDelay        time.Duration `mapstructure:"requeue_delay"`
	RequestsPerSecond   int           `mapstructure:"requests_per_second"`
	RequestsBurst       int           `mapstructure:"requests_burst"`
	UseSolidity         bool          `mapstructure:"use_solidity"`
	IsActive            bool          `mapstructure:"is_active"`
}

type MonitoringConfig struct {
	ScanWindow          int           `mapstructure:"scan_window"`
	PollInterval        time.Duration `mapstructure:"poll_interval"`
	WSRetryInterval     time.Duration `mapstructure:"ws_retry_interval"`
	HealthCheckInterval time.Duration `mapstructure:"health_check_interval"`
	MetricsPort         int           `mapstructure:"metrics_port"`
}

type LoggingConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

func Load() (*Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("./config")
	viper.AddConfigPath(".")

	// Environment variable overrides
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Set defaults
	setDefaults()

	// Override with environment variables
	overrideWithEnv()

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	normalizeConfig(&config)
	if err := validateConfig(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

func setDefaults() {
	viper.SetDefault("server.port", 8080)
	viper.SetDefault("server.read_timeout", "30s")
	viper.SetDefault("server.write_timeout", "30s")

	viper.SetDefault("redis.url", "redis://localhost:6379")
	viper.SetDefault("redis.pool_size", 20)     // Reduced for lower memory usage
	viper.SetDefault("redis.min_idle_conns", 5) // Reduced for lower memory usage

	viper.SetDefault("rabbitmq.exchange", "blockchain.events")
	viper.SetDefault("rabbitmq.queue_prefix", "bloop")
	viper.SetDefault("rabbitmq.prefetch_count", 100)

	viper.SetDefault("tron.api_url", "https://api.trongrid.io")
	viper.SetDefault("tron.confirmations", 1)
	viper.SetDefault("tron.batch_size", 25)
	viper.SetDefault("tron.max_concurrent_blocks", 5)
	viper.SetDefault("tron.rpc_timeout", "30s")
	viper.SetDefault("tron.retry_attempts", 3)
	viper.SetDefault("tron.retry_delay", "2s")
	viper.SetDefault("tron.requeue_delay", "5s")
	viper.SetDefault("tron.requests_per_second", 5)
	viper.SetDefault("tron.requests_burst", 10)
	viper.SetDefault("tron.use_solidity", true)
	viper.SetDefault("tron.is_active", false)

	viper.SetDefault("monitoring.scan_window", 1000)
	viper.SetDefault("monitoring.poll_interval", "15s")
	viper.SetDefault("monitoring.ws_retry_interval", "30s")
	viper.SetDefault("monitoring.health_check_interval", "30s")
	viper.SetDefault("monitoring.metrics_port", 9090)

	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.format", "json")
}

func overrideWithEnv() {
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		viper.Set("redis.url", redisURL)
	}
	if rabbitURL := os.Getenv("RABBITMQ_URL"); rabbitURL != "" {
		viper.Set("rabbitmq.url", rabbitURL)
	}
	if tronURLs := os.Getenv("TRON_API_URLS"); tronURLs != "" {
		viper.Set("tron.api_urls", strings.Split(tronURLs, ","))
	}
	if tronURL := os.Getenv("TRON_API_URL"); tronURL != "" {
		viper.Set("tron.api_url", tronURL)
		viper.Set("tron.api_urls", []string{tronURL})
	}
	if tronAPIKey := os.Getenv("TRON_API_KEY"); tronAPIKey != "" {
		viper.Set("tron.api_key", tronAPIKey)
	}
	if logLevel := os.Getenv("LOG_LEVEL"); logLevel != "" {
		viper.Set("logging.level", logLevel)
	}
}

func (c *Config) ConfiguredNetworks() []types.BlockchainType {
	networks := []types.BlockchainType{types.Tron}
	for _, chain := range c.EVM {
		networks = append(networks, chain.Network)
	}
	for _, chain := range c.UTXO {
		networks = append(networks, chain.Network)
	}
	return networks
}

func normalizeConfig(cfg *Config) {
	for i := range cfg.EVM {
		normalizeEVMConfig(&cfg.EVM[i])
	}
	for i := range cfg.UTXO {
		normalizeUTXOConfig(&cfg.UTXO[i])
	}
}

func normalizeEVMConfig(cfg *EVMConfig) {
	cfg.Network = types.BlockchainType(strings.ToUpper(strings.TrimSpace(string(cfg.Network))))
	cfg.NativeCurrency = types.Currency(strings.ToUpper(strings.TrimSpace(string(cfg.NativeCurrency))))
	cfg.RPCURLs = compactStrings(cfg.RPCURLs)
	cfg.OminiRPCURLs = compactStrings(cfg.OminiRPCURLs)
	cfg.WSURL = strings.TrimSpace(cfg.WSURL)
	cfg.OminiRPCURL = strings.TrimSpace(cfg.OminiRPCURL)

	if cfg.Confirmations == 0 {
		cfg.Confirmations = 5
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 25
	}
	if cfg.MaxConcurrentBlocks == 0 {
		cfg.MaxConcurrentBlocks = 5
	}
	if cfg.RPCTimeout == 0 {
		cfg.RPCTimeout = 30 * time.Second
	}
	if cfg.RetryAttempts == 0 {
		cfg.RetryAttempts = 3
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = 2 * time.Second
	}
	if strings.TrimSpace(cfg.BlockFetchMode) == "" {
		cfg.BlockFetchMode = "full"
	}
	if cfg.TxFetchConcurrency == 0 {
		cfg.TxFetchConcurrency = 10
	}
	if cfg.OminiRequestsPerSecond == 0 {
		cfg.OminiRequestsPerSecond = 5
	}
	if cfg.OminiRequestsBurst == 0 {
		cfg.OminiRequestsBurst = 10
	}
}

func normalizeUTXOConfig(cfg *UTXOConfig) {
	cfg.Network = types.BlockchainType(strings.ToUpper(strings.TrimSpace(string(cfg.Network))))
	cfg.NativeCurrency = types.Currency(strings.ToUpper(strings.TrimSpace(string(cfg.NativeCurrency))))
	cfg.APIURL = strings.TrimSpace(cfg.APIURL)
	cfg.APIURLs = compactStrings(cfg.APIURLs)
	cfg.WSURL = strings.TrimSpace(cfg.WSURL)

	if cfg.Confirmations == 0 {
		cfg.Confirmations = 1
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 10
	}
	if cfg.MaxConcurrentBlocks == 0 {
		cfg.MaxConcurrentBlocks = 5
	}
	if cfg.RPCTimeout == 0 {
		cfg.RPCTimeout = 30 * time.Second
	}
	if cfg.RetryAttempts == 0 {
		cfg.RetryAttempts = 3
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = 2 * time.Second
	}
	if cfg.TxFetchConcurrency == 0 {
		cfg.TxFetchConcurrency = 2
	}
	if cfg.RequeueDelay == 0 {
		cfg.RequeueDelay = 5 * time.Second
	}
	if cfg.RequestsPerSecond == 0 {
		cfg.RequestsPerSecond = 5
	}
	if cfg.RequestsBurst == 0 {
		cfg.RequestsBurst = 10
	}
}

func validateConfig(cfg *Config) error {
	seenEVM := make(map[types.BlockchainType]struct{}, len(cfg.EVM))
	for i := range cfg.EVM {
		chain := cfg.EVM[i]
		if chain.Network == "" {
			return fmt.Errorf("evm[%d].network is required", i)
		}
		if _, ok := seenEVM[chain.Network]; ok {
			return fmt.Errorf("duplicate evm network %q", chain.Network)
		}
		seenEVM[chain.Network] = struct{}{}

		if chain.NativeCurrency == "" {
			return fmt.Errorf("evm[%d].native_currency is required", i)
		}
		if chain.IsActive && len(chain.RPCURLs) == 0 {
			return fmt.Errorf("evm[%d] (%s) requires at least one rpc_url when active", i, chain.Network)
		}
		if mode := strings.ToLower(strings.TrimSpace(chain.BlockFetchMode)); mode != "full" && mode != "light" {
			return fmt.Errorf("evm[%d] (%s) has invalid block_fetch_mode %q", i, chain.Network, chain.BlockFetchMode)
		}
		for j, token := range chain.Tokens {
			if strings.TrimSpace(token.Currency) == "" {
				return fmt.Errorf("evm[%d] (%s) token[%d].currency is required", i, chain.Network, j)
			}
			if strings.TrimSpace(token.Contract) == "" {
				return fmt.Errorf("evm[%d] (%s) token[%d].contract is required", i, chain.Network, j)
			}
			if !common.IsHexAddress(token.Contract) {
				return fmt.Errorf("evm[%d] (%s) token[%d].contract is not a valid EVM address", i, chain.Network, j)
			}
			if token.Decimals < 0 {
				return fmt.Errorf("evm[%d] (%s) token[%d].decimals cannot be negative", i, chain.Network, j)
			}
		}
	}

	seenUTXO := make(map[types.BlockchainType]struct{}, len(cfg.UTXO))
	for i := range cfg.UTXO {
		chain := cfg.UTXO[i]
		if chain.Network == "" {
			return fmt.Errorf("utxo[%d].network is required", i)
		}
		if _, ok := seenUTXO[chain.Network]; ok {
			return fmt.Errorf("duplicate utxo network %q", chain.Network)
		}
		seenUTXO[chain.Network] = struct{}{}

		if chain.NativeCurrency == "" {
			return fmt.Errorf("utxo[%d].native_currency is required", i)
		}
		if chain.IsActive && len(chain.APIURLs) == 0 && chain.APIURL == "" {
			return fmt.Errorf("utxo[%d] (%s) requires at least one api_url when active", i, chain.Network)
		}
	}

	return nil
}

func compactStrings(values []string) []string {
	compacted := values[:0]
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			compacted = append(compacted, value)
		}
	}
	return compacted
}
