# Release Notes — v0.2.0

## Highlights

- Added multi-provider Bitcoin API support with automatic failover (Esplora + Blockchain.com).
- Switched BTC block fetching to batch/paged endpoints to reduce per-transaction API calls.
- Expanded tracker stats with provider usage, error counts, queue health, and gap visibility.

## Changes

- Bitcoin API adapters:
  - Esplora client with paged block transaction fetching.
  - Blockchain.com Explorer client using `/latestblock`, `/block-height`, `/rawblock`.
  - Multi-client with round-robin + failover.
- Tracker stats:
  - Added provider usage/error counts and last provider used.
  - Added in-flight tx count, block gap, queue depth, and skipped-enqueue counters.
- Config:
  - Added `bitcoin.api_urls` for multi-provider configuration.
  - Updated defaults for rate limiting and concurrency.

## Config Example

```yaml
bitcoin:
  api_urls:
    - "https://mempool.space/api"
    - "https://blockchain.info"
```
