# Release Notes — v0.6.0

## Highlights

- Added Solana (`SOLANA`) tracker support for native SOL transfers.
- Added configurable SPL token monitoring for Solana using configured token mint addresses.
- Added Solana JSON-RPC provider rotation, request rate limiting, and websocket slot subscriptions.
- Added instruction-level Solana transfer parsing for system transfers and SPL `transfer` / `transferChecked` instructions.
- Added Token-2022 transfer parsing support.
- Added fallback SPL token balance-delta detection when transfer instructions are unavailable.

## Configuration

- Added a new top-level `solana` config block.
- `solana.tokens[].contract` is the SPL token mint address.
- Solana defaults to `confirmations: 32`.
- Solana can be enabled with `solana.is_active: true`.

## Notes

- Skipped Solana slots are treated as normal and marked processed to avoid retry loops.
- Public Solana RPC endpoints can rate-limit aggressively; production deployments should use dedicated provider keys where possible.

# Release Notes — v0.5.0

## Breaking Changes

- Configuration now uses `evm` as a list of chain configs instead of top-level `ethereum` and `bsc` sections.
- Configuration now uses `utxo` as a list of UTXO chain configs instead of the top-level `bitcoin` section.
- Token monitoring config now uses `tokens` arrays. Replace `usdt_contract` and `usdt_decimals` with entries containing `currency`, `contract`, and `decimals`.
- EVM block fetching now defaults to `block_fetch_mode: full`; set `block_fetch_mode: light` only for providers that cannot return full block payloads reliably.
- Consumers and operators should treat supported network values as explicit chain IDs such as `ETH`, `BSC`, `POLYGON`, `TRON`, `BTC`, and `LTC`.

## Highlights

- Added Litecoin (`LTC`) support through the shared UTXO tracker path.
- Added list-based EVM configuration for Ethereum, BSC, and Polygon.
- Added list-based UTXO configuration for Bitcoin and Litecoin.
- Added configurable token arrays for EVM and TRON networks, enabling assets beyond USDT such as USDC.
- Added activation flags per configured EVM/UTXO network so inactive chains can remain documented without starting trackers.

## Migration Notes

- Move Ethereum and BSC settings under `evm`, and include `network` plus `native_currency` for each entry.
- Move Bitcoin settings under `utxo`, and include `network: "BTC"` plus `native_currency: "BTC"`.
- Add Litecoin by appending a UTXO entry with `network: "LTC"`, `native_currency: "LTC"`, and Litecoin API URLs such as `https://litecoinspace.org/api`.
- Update token settings to the new array format:

```yaml
tokens:
  - currency: "USDT"
    contract: "<contract-address>"
    decimals: 6
```

## Config Example

```yaml
evm:
  - network: "ETH"
    native_currency: "ETH"
    rpc_urls:
      - "https://ethereum-sepolia-rpc.publicnode.com"
    tokens:
      - currency: "USDT"
        contract: "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
        decimals: 6
    block_fetch_mode: full
    is_active: true

utxo:
  - network: "BTC"
    native_currency: "BTC"
    api_urls:
      - "https://mempool.space/testnet/api"
    is_active: false
  - network: "LTC"
    native_currency: "LTC"
    api_urls:
      - "https://litecoinspace.org/api"
    confirmations: 6
    is_active: false
```

# Release Notes — v0.4.1

## Fixed

- Prevented BTC high-water mark drift from skipping unprocessed lower blocks.
- Clamped `last_processed` to the safe head when stored progress gets ahead.
- Advanced `last_processed` only through contiguous processed block markers.
- Improved BTC block recovery so missed blocks are picked up reliably by polling and catchup.

## Changed

- BTC tracker now increments processed transaction metrics per fully processed block for parity with Ethereum tracker metrics.

# Release Notes — v0.4.0

## Highlights

- Added TRON network support (`TRON`) and native currency support (`TRX`).
- Added TRON wallet watching for native TRX transfers and TRC20 USDT transfer events.
- Added TRON HTTP client support for multiple provider URLs, TronGrid API keys, retries, request rate limiting, and provider stats.
- Added TRON address normalization for Base58 and hex `41...` addresses.
- Added per-block TRON transaction-info fetching to reduce RPC usage.
- Added TRON config defaults, example config, startup wiring, and watched-wallet Bloom filter seeding.
- Added unit tests for TRON address conversion and token amount parsing.

## Notes

- TRON uses `walletsolidity` endpoints by default for safer confirmed-block tracking.
- Default TRON config uses the mainnet TRC20 USDT contract.
- For testnet, change the TRON API URL and use a testnet TRC20 token contract.
- Keep mainnet and testnet Redis data separate because wallet and block keys are keyed by `TRON`, not by environment.

# Release Notes — v0.3.0

## Highlights

- Added Prometheus tracker metrics at `GET /metrics` on `monitoring.metrics_port` (default `9090`).
- Added tracker metrics by network for block gap, last processed block, safe head, current block height, block queue length, error count, skipped full channels, skipped processed blocks, throughput, and uptime.

## Changed

- BTC block processing now fails when deposit or withdrawal publishing fails.
- Publish failures now bubble up so the block is retried instead of incorrectly advancing after a failed event publish.

# Release Notes — v0.2.2

## Fixed

- BTC now uses configured confirmation counts.
- BTC input parsing was refactored to improve withdrawal event handling.
- Added logging for skipped multi-wallet BTC cases.

# Release Notes — v0.2.1

## Highlights

- BTC deposit events now include `inputs` for UTXO source addresses and amounts.

## Changes

- Wallet deposits: Added `inputs` array for BTC deposits (UTXO source address + amount).

# Release Notes — v0.2.0

## Highlights

- Added multi-provider Bitcoin API support with automatic failover (Esplora + Blockchain.com).
- Switched BTC block fetching to batch/paged endpoints to reduce per-transaction API calls.
- Expanded tracker stats with provider usage, error counts, queue health, and gap visibility.

## Changes

- Bitcoin API adapters: Esplora client with paged block transaction fetching.
- Bitcoin API adapters: Blockchain.com Explorer client using `/latestblock`, `/block-height`, `/rawblock`.
- Bitcoin API adapters: Multi-client with round-robin + failover.
- Tracker stats: Added provider usage/error counts and last provider used.
- Tracker stats: Added in-flight tx count, block gap, queue depth, and skipped-enqueue counters.
- Config: Added `bitcoin.api_urls` for multi-provider configuration.
- Config: Updated defaults for rate limiting and concurrency.

## Config Example

```yaml
bitcoin:
  api_urls:
    - "https://mempool.space/api"
    - "https://blockchain.info"
```
