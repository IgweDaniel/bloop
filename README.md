# Bloop Blockchain Monitor

A high-performance, standalone blockchain monitoring service written in Go. Supports Ethereum, Binance Smart Chain, TRON, Bitcoin, and Litecoin with native coin monitoring plus configurable token contracts such as USDT and USDC.

## Features

- **High Performance**: Built in Go with concurrent processing and optimized RPC handling
- **Circuit Breakers**: Automatic failover between RPC providers with health monitoring
- **Loose Coupling**: Pluggable messaging system (RabbitMQ, webhooks)
- **Extensible**: Clean architecture for adding new blockchain networks
- **Production Ready**: Docker support, comprehensive logging, metrics
- **Real-time**: WebSocket connections with polling fallback

## Architecture

The architecture is organized into several core components:

- **HTTP API**: Exposes endpoints for health checks, wallet management, and statistics.
- **Tracker Manager**: Coordinates the tracking of blockchain activity using a shared base tracker for common logic.
- **Blockchain Processors**: Specialized modules for each supported blockchain (e.g., Ethereum, Bitcoin), responsible for RPC client management, transaction parsing, and block fetching.
- **Messaging**: Pluggable system for publishing events, supporting RabbitMQ, webhooks, and other integrations.
- **Storage**: Utilizes Redis and in-memory caching for fast access and persistence.

This composition-based approach allows most logic to be shared across blockchains, with only the processor modules needing customization for each network.

### Shared Base Tracker Handles:

- ✅ Block processing orchestration
- ✅ Concurrency management
- ✅ Health monitoring & metrics
- ✅ Event publishing
- ✅ Catchup logic & confirmations
- ✅ Graceful shutdown

### Blockchain Processors Handle:

- 🔧 RPC client management
- 🔧 Transaction parsing
- 🔧 Block fetching
- 🔧 Address validation

## Quick Start

### Prerequisites

- Go 1.24+
- Docker & Docker Compose
- Redis
- RabbitMQ

### Using Docker Compose (Recommended)

```bash
# Clone and start all services
git clone <repo>
cd bloop
make docker-run
```

### Manual Setup

1. **Start infrastructure services:**

```bash
make infra-up
```

2. **Build and run the monitor:**

```bash
make build
./bin/bloop-monitor
```

3. **Start the test listener (in another terminal):**

```bash
make test-listener
```

4. **Add wallets to watch:**

```bash
# Watch an Ethereum wallet
curl -X POST http://localhost:8080/api/v1/wallets/watch \
  -H "Content-Type: application/json" \
  -d '{"network": "ETH", "address": "0x742d35Cc6634C0532925a3b8D400E4C0d5C7c6B4", "wallet_id": "user-123"}'

# Watch a Litecoin wallet
curl -X POST http://localhost:8080/api/v1/wallets/watch \
  -H "Content-Type: application/json" \
  -d '{"network": "LTC", "address": "ltc1qexampleaddress", "wallet_id": "user-456"}'
```

### Docker Compose starts:

- Redis (port 6379)
- RabbitMQ (port 5672, management UI on 15672)
- Bloop Monitor (port 8080)

### Local Development

```bash
# Start dependencies
make dev-setup
docker-compose up -d redis rabbitmq

# Run the monitor
make run
```

## Configuration

Configuration via `config/config.yaml` or environment variables:

```yaml
ethereum:
  rpc_urls:
    - "https://eth.llamarpc.com"
    - "https://rpc.ankr.com/eth"
  ws_url: "wss://eth.llamarpc.com"
  tokens:
    - currency: "USDT"
      contract: "0xdAC17F958D2ee523a2206206994597C13D831ec7"
      decimals: 6
    - currency: "USDC"
      contract: "<usdc-contract-address>"
      decimals: 6
  confirmations: 5
  batch_size: 50
  max_concurrent_blocks: 5

utxo:
  - network: "BTC"
    native_currency: "BTC"
    api_urls:
      - "https://mempool.space/testnet/api"
      - "https://blockstream.info/testnet/api"
    confirmations: 1
    batch_size: 10
    max_concurrent_blocks: 5
  - network: "LTC"
    native_currency: "LTC"
    api_urls:
      - "https://litecoinspace.org/api"
    confirmations: 6
    batch_size: 10
    max_concurrent_blocks: 5

redis:
  url: "redis://localhost:6379"
  pool_size: 100

rabbitmq:
  url: "amqp://bloop:bloop123@localhost:5672/"
  exchange: "blockchain.events"
```

### Environment Variables

```bash
ETH_RPC_URLS="https://eth.llamarpc.com,https://rpc.ankr.com/eth"
ETH_WS_URL="wss://eth.llamarpc.com"
REDIS_URL="redis://localhost:6379"
RABBITMQ_URL="amqp://bloop:bloop123@localhost:5672/"
LOG_LEVEL="info"
```

## API Endpoints

### Health Check

```bash
curl http://localhost:8080/health
```

### Prometheus Metrics

```bash
curl http://localhost:9090/metrics
```

Key tracker metrics exposed per network label:

- `bloop_tracker_block_gap`
- `bloop_tracker_last_processed_block`
- `bloop_tracker_safe_head`
- `bloop_tracker_current_block_height`
- `bloop_tracker_block_queue_len`
- `bloop_tracker_error_count_total`
- `bloop_tracker_skipped_channel_full_total`
- `bloop_tracker_skipped_processed_total`

### Add Watched Wallet

```bash
curl -X POST http://localhost:8080/api/v1/wallets/watch \
  -H "Content-Type: application/json" \
  -d '{
    "network": "ETH",
    "address": "0x742d35Cc6634C0532925a3b8D400E4C0d5C7c6B4",
    "wallet_id": "user-123"
  }'
```

### Remove Watched Wallet

```bash
curl -X DELETE http://localhost:8080/api/v1/wallets/unwatch \
  -H "Content-Type: application/json" \
  -d '{
    "network": "ETH",
    "address": "0x742d35Cc6634C0532925a3b8D400E4C0d5C7c6B4"
  }'
```

### Get Tracker Stats

```bash
curl http://localhost:8080/api/v1/trackers/stats
```

### Get Supported Networks

```bash
curl http://localhost:8080/api/v1/networks
```

### EVM Block Fetching

By default, EVM trackers use `block_fetch_mode: full`, which calls `eth_getBlockByNumber` with full transactions on the regular RPC pool. The processor scans the returned transaction objects first, then fetches receipts only for transactions that can matter:

- Plain native transfers where `to` or `from` is watched.
- Direct calls to configured token contracts.

Use `block_fetch_mode: light` only if a provider cannot serve full block payloads reliably. In light mode, the tracker fetches transaction hashes first and then fetches each transaction individually.

`omini_rpc_urls` are optional fallback RPCs for full block fetch failures. Regular RPCs are tried first.

### Get Watched Wallets

```bash
# Get all watched wallets for all networks
curl http://localhost:8080/api/v1/wallets

# Get watched wallets for specific network
curl http://localhost:8080/api/v1/wallets?network=ETH
```

## Test Listener

The project includes a test listener that connects to RabbitMQ and logs deposit events in real-time:

```bash
# Build and run the test listener
make test-listener

# Or run it directly
./bin/test-listener
```

The test listener will:

- 🎧 Connect to RabbitMQ and listen for deposit events
- 💰 Log detailed deposit information when transactions are detected
- 🌈 Display colorful, formatted output for easy monitoring
- 📊 Show network, amount, wallet ID, transaction hash, and more

### Sample Output

```
💰 DEPOSIT DETECTED!
================================================================================
🚨 DEPOSIT ALERT 🚨
================================================================================
🌐 Network:      ETH
💎 Currency:     ETH
💰 Amount:       1.5
📍 Address:      0x742d35Cc6634C0532925a3b8D400E4C0d5C7c6B4
👤 Wallet ID:    user-123
🔗 Tx Hash:      0xabc123...
📦 Block:        18500000
⏰ Time:         2023-12-01 14:30:25
⛽ Gas Used:     21000
💸 Gas Price:    20.5 gwei
================================================================================
```

## Event Messages

When deposits are detected, events are published to RabbitMQ:

```json
{
  "type": "wallet.deposit",
  "payload": {
    "tx_hash": "0x...",
    "wallet_id": "user-123",
    "wallet_address": "0x742d35Cc6634C0532925a3b8D400E4C0d5C7c6B4",
    "from_address": "0x...",
    "amount": "1.5",
    "currency": "ETH",
    "network": "ETH",
    "block_number": 18500000,
    "confirmations": 5,
    "timestamp": "2023-11-15T10:30:00Z",
    "network_fee": "0.001",
    "status": "CONFIRMED"
  },
  "timestamp": "2023-11-15T10:30:00Z",
  "source": "ETH"
}
```

### Key Optimizations

1. **Concurrent Processing**: Goroutines for parallel block/transaction processing
2. **Circuit Breakers**: Automatic RPC provider failover
3. **Batch Operations**: Redis pipelining and RPC batching
4. **Smart Caching**: Multi-level caching with TTL
5. **Rate Limiting**: Configurable RPC rate limits
6. **Connection Pooling**: Optimized Redis and RPC connections

## Extending to New Blockchains

The architecture is designed for easy extension:

1. **Implement Tracker Interface**:

```go
type Tracker interface {
    Start(ctx context.Context) error
    Stop() error
    AddWatchedWallet(ctx context.Context, address, walletID string) error
    RemoveWatchedWallet(ctx context.Context, address string) error
    GetNetwork() types.BlockchainType
    IsRunning() bool
    GetStats() TrackerStats
}
```

2. **Add to Factory**:

```go
func (f *DefaultTrackerFactory) CreateTracker(network types.BlockchainType) (Tracker, error) {
    switch network {
    case types.Ethereum:
        return f.createEthereumTracker()
    case types.Bitcoin:
        return f.createBitcoinTracker() // Your implementation
    }
}
```

3. **Update Configuration**:

```yaml
bitcoin:
  rpc_url: "https://bitcoin-rpc.com"
  confirmations: 6

utxo:
  - network: "LTC"
    native_currency: "LTC"
    api_urls:
      - "https://litecoinspace.org/api"
    confirmations: 6
```

## Development

### Project Structure

```
bloop/
├── cmd/monitor/          # Main application
├── internal/
│   ├── api/             # HTTP API handlers
│   ├── blockchain/      # Blockchain trackers
│   │   └── ethereum/    # Ethereum implementation
│   ├── config/          # Configuration management
│   ├── messaging/       # RabbitMQ/webhook publishers
│   ├── storage/         # Redis storage layer
│   └── types/           # Shared types
├── config/              # Configuration files
└── docker-compose.yml   # Development environment
```

### Commands

```bash
make build          # Build binary
make test           # Run tests
make lint           # Run linter
make fmt            # Format code
make docker-run     # Run with Docker
make dev            # Development mode
```

## Monitoring & Observability

### Logs

Structured JSON logging with configurable levels:

```json
{
  "level": "info",
  "msg": "ETH deposit detected",
  "tx_hash": "0x...",
  "wallet_id": "user-123",
  "amount": "1.5",
  "currency": "ETH",
  "block_number": 18500000,
  "time": "2023-11-15T10:30:00Z"
}
```

### Metrics (TODO)

- Blocks processed per second
- Transaction processing latency
- RPC provider health
- Memory/CPU usage

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests
5. Run `make lint` and `make test`
6. Submit a pull request

## License

MIT License - see LICENSE file for details.
