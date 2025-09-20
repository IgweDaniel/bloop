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
	RPCURLs             []string      `mapstructure:"rpc_urls"`
	WSURL               string        `mapstructure:"ws_url"`
	OminiRPCURL         string        `mapstructure:"omini_rpc_url"`
	USDTContract        string        `mapstructure:"usdt_contract"`
	Confirmations       int           `mapstructure:"confirmations"`
	BatchSize           int           `mapstructure:"batch_size"`
	MaxConcurrentBlocks int           `mapstructure:"max_concurrent_blocks"`
	RPCTimeout          time.Duration `mapstructure:"rpc_timeout"`
	RetryAttempts       int           `mapstructure:"retry_attempts"`
	RetryDelay          time.Duration `mapstructure:"retry_delay"`
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
	viper.SetDefault("redis.pool_size", 100)
	viper.SetDefault("redis.min_idle_conns", 10)

	viper.SetDefault("rabbitmq.exchange", "blockchain.events")
	viper.SetDefault("rabbitmq.queue_prefix", "bloop")
	viper.SetDefault("rabbitmq.prefetch_count", 100)

	viper.SetDefault("ethereum.confirmations", 5)
	viper.SetDefault("ethereum.batch_size", 50)
	viper.SetDefault("ethereum.max_concurrent_blocks", 5)
	viper.SetDefault("ethereum.rpc_timeout", "30s")
	viper.SetDefault("ethereum.retry_attempts", 3)
	viper.SetDefault("ethereum.retry_delay", "2s")

	viper.SetDefault("bitcoin.confirmations", 1)
	viper.SetDefault("bitcoin.batch_size", 20)
	viper.SetDefault("bitcoin.max_concurrent_blocks", 2)
	viper.SetDefault("bitcoin.rpc_timeout", "30s")
	viper.SetDefault("bitcoin.retry_attempts", 3)
	viper.SetDefault("bitcoin.retry_delay", "2s")
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
