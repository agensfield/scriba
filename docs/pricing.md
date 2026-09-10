# Pricing catalog workflow

Scriba calculates costs from `internal/pricing/catalog.json`. The catalog is
embedded in the binary, so runtime pricing is deterministic and never performs
network requests. Decimal strings preserve the reviewed source notation before
conversion to per-token rates.

The catalog is an as-of estimate of Standard-tier API pricing, not a historical
invoice ledger and not ChatGPT subscription billing. Its source receipt records
the exact review time and official OpenAI pages. As of 2026-09-10, GPT-5.6
Sol/Terra/Luna use the current Standard short- and long-context tables; frozen
third-party differential receipts retain their original capture-time rates.

Run the offline integrity check with:

```sh
go run ./scripts/pricing-check
```

For an update, first save or manually prepare source data, then generate a
candidate:

```sh
go run ./scripts/pricing-refresh -source /path/to/proposed.json
```

The refresh command refuses to write the runtime catalog. A maintainer must
compare the candidate's effective date, source URL, bound source-receipt hash, model names, aliases,
thresholds, and every short/long input, cached-input, output, and cache-write
rate against the primary pricing page. Only then should they replace the
catalog and run the checker plus `go test ./...`. Tests and required checks must
not fetch pricing from the network.
