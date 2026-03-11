package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Server     ServerConfig     `mapstructure:"server"`
	Redis      RedisConfig      `mapstructure:"redis"`
	RabbitMQ   RabbitMQConfig   `mapstructure:"rabbitmq"`
	Ethereum   EthereumConfig   `mapstructure:"ethereum"`
	Bitcoin    BitcoinConfig    `mapstructure:"bitcoin"`
	Bsc        EthereumConfig   `mapstructure:"bsc"`
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

type EthereumConfig struct {
	RPCURLs                []string      `mapstructure:"rpc_urls"`
	WSURL                  string        `mapstructure:"ws_url"`
	OminiRPCURL            string        `mapstructure:"omini_rpc_url"`             // Deprecated: use OminiRPCURLs
	OminiRPCURLs           []string      `mapstructure:"omini_rpc_urls"`            // Multiple omini RPC URLs for rotation
	OminiRequestsPerSecond int           `mapstructure:"omini_requests_per_second"` // Rate limit for omini clients
	OminiRequestsBurst     int           `mapstructure:"omini_requests_burst"`      // Burst capacity for omini clients
	USDTContract           string        `mapstructure:"usdt_contract"`
	USDTDecimals           int32         `mapstructure:"usdt_decimals"`
	Confirmations          int           `mapstructure:"confirmations"`
	BatchSize              int           `mapstructure:"batch_size"`
	MaxConcurrentBlocks    int           `mapstructure:"max_concurrent_blocks"`
	RPCTimeout             time.Duration `mapstructure:"rpc_timeout"`
	RetryAttempts          int           `mapstructure:"retry_attempts"`
	RetryDelay             time.Duration `mapstructure:"retry_delay"`
	BlockFetchMode         string        `mapstructure:"block_fetch_mode"`     // "light" or "full" - light uses GetBlockVerbose with RPC rotation
	TxFetchConcurrency     int           `mapstructure:"tx_fetch_concurrency"` // Concurrency for fetching individual transactions in light mode
	IsActive               bool          `mapstructure:"is_active"`
	SkipNative             bool          `mapstructure:"skip_native"` // if true, skip checking native; if false (zero value), check native
}

// BitcoinConfig contains Bitcoin-specific configuration
type BitcoinConfig struct {
	APIURL              string        `mapstructure:"api_url"`
	WSURL               string        `mapstructure:"ws_url"`
	Confirmations       int           `mapstructure:"confirmations"`
	BatchSize           int           `mapstructure:"batch_size"`
	MaxConcurrentBlocks int           `mapstructure:"max_concurrent_blocks"`
	RPCTimeout          time.Duration `mapstructure:"rpc_timeout"`
	RetryAttempts       int           `mapstructure:"retry_attempts"`
	RetryDelay          time.Duration `mapstructure:"retry_delay"`
	TxFetchConcurrency  int           `mapstructure:"tx_fetch_concurrency"`
	RequeueDelay        time.Duration `mapstructure:"requeue_delay"`
	RequestsPerSecond   int           `mapstructure:"requests_per_second"`
	RequestsBurst       int           `mapstructure:"requests_burst"`
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

	return &config, nil
}

func setDefaults() {
	viper.SetDefault("server.port", 8080)
	viper.SetDefault("server.read_timeout", "30s")
	viper.SetDefault("server.write_timeout", "30s")

	viper.SetDefault("redis.url", "redis://localhost:6379")
	viper.SetDefault("redis.pool_size", 20)      // Reduced for lower memory usage
	viper.SetDefault("redis.min_idle_conns", 5)  // Reduced for lower memory usage

	viper.SetDefault("rabbitmq.exchange", "blockchain.events")
	viper.SetDefault("rabbitmq.queue_prefix", "bloop")
	viper.SetDefault("rabbitmq.prefetch_count", 100)

	viper.SetDefault("ethereum.confirmations", 5)
	viper.SetDefault("ethereum.batch_size", 25)              // Reduced from 50 for lower memory
	viper.SetDefault("ethereum.max_concurrent_blocks", 5)    // Keep at 5 for memory efficiency
	viper.SetDefault("ethereum.rpc_timeout", "30s")
	viper.SetDefault("ethereum.retry_attempts", 3)
	viper.SetDefault("ethereum.retry_delay", "2s")
	viper.SetDefault("ethereum.block_fetch_mode", "light")   // Light mode uses less memory
	viper.SetDefault("ethereum.tx_fetch_concurrency", 10)    // Reduced from 20
	viper.SetDefault("ethereum.omini_requests_per_second", 5)
	viper.SetDefault("ethereum.omini_requests_burst", 10)

	viper.SetDefault("bitcoin.confirmations", 1)
	viper.SetDefault("bitcoin.batch_size", 10)               // Reduced from 20
	viper.SetDefault("bitcoin.max_concurrent_blocks", 2)
	viper.SetDefault("bitcoin.rpc_timeout", "30s")
	viper.SetDefault("bitcoin.retry_attempts", 3)
	viper.SetDefault("bitcoin.retry_delay", "2s")
	viper.SetDefault("bitcoin.tx_fetch_concurrency", 10)     // Reduced from 20
	viper.SetDefault("bitcoin.requeue_delay", "5s")
	viper.SetDefault("bitcoin.requests_per_second", 3)
	viper.SetDefault("bitcoin.requests_burst", 5)
	viper.SetDefault("bitcoin.ws_url", "")
	viper.SetDefault("bitcoin.api_url", "")

	viper.SetDefault("monitoring.scan_window", 1000)
	viper.SetDefault("monitoring.poll_interval", "15s")
	viper.SetDefault("monitoring.ws_retry_interval", "30s")
	viper.SetDefault("monitoring.health_check_interval", "30s")
	viper.SetDefault("monitoring.metrics_port", 9090)

	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.format", "json")
}

func overrideWithEnv() {
	if urls := os.Getenv("ETH_RPC_URLS"); urls != "" {
		viper.Set("ethereum.rpc_urls", strings.Split(urls, ","))
	}
	if wsURL := os.Getenv("ETH_WS_URL"); wsURL != "" {
		viper.Set("ethereum.ws_url", wsURL)
	}
	if contract := os.Getenv("USDT_CONTRACT_ADDRESS"); contract != "" {
		viper.Set("ethereum.usdt_contract", contract)
	}
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		viper.Set("redis.url", redisURL)
	}
	if rabbitURL := os.Getenv("RABBITMQ_URL"); rabbitURL != "" {
		viper.Set("rabbitmq.url", rabbitURL)
	}
	if btcURL := os.Getenv("BTC_RPC_URL"); btcURL != "" {
		viper.Set("bitcoin.rpc_url", btcURL)
	}
	if btcWS := os.Getenv("BTC_WS_URL"); btcWS != "" {
		viper.Set("bitcoin.ws_url", btcWS)
	}
	if btcAPI := os.Getenv("BTC_API_URL"); btcAPI != "" {
		viper.Set("bitcoin.api_url", btcAPI)
	}
	if btcUser := os.Getenv("BTC_RPC_USER"); btcUser != "" {
		viper.Set("bitcoin.username", btcUser)
	}
	if btcPass := os.Getenv("BTC_RPC_PASS"); btcPass != "" {
		viper.Set("bitcoin.password", btcPass)
	}
	if logLevel := os.Getenv("LOG_LEVEL"); logLevel != "" {
		viper.Set("logging.level", logLevel)
	}
}
