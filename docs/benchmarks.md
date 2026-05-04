# Benchmarks

The alpha benchmark is intentionally light. Its job is to establish a repeatable
baseline against `ccusage`/`openusage` behavior without turning the benchmark
itself into a denial of laptop.

## Default Safe Mode

```sh
bun run scriba bench ccusage --provider all
```

This does not run `ccusage`. It reports:

- provider data paths
- JSONL file count
- total bytes
- missing directories
- the bounded reference command plan

Current local safe-mode observation on 2026-05-05:

- Claude: 1,665 JSONL files, 834,608,688 bytes.
- Codex: 776 JSONL files, 4,711,324,647 bytes.

## Executing the Baseline

```sh
bun run scriba bench ccusage --provider codex --execute --timeout-ms 30000
```

Execution is sequential. Each command has its own timeout. Stdout/stderr samples
are capped so a broken reference command cannot flood the agent context.

The current command set is deliberately small:

- Claude: `daily`, `weekly`, `monthly`, `session`, `blocks`
- Codex: `daily`, `monthly`, `session`

Do not add exhaustive command matrices until Scriba has its own stable
performance profile and fixtures large enough to reproduce the memory problem.
