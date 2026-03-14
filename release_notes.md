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
