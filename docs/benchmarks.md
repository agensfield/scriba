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
- Codex: 776 JSONL files, 4,711,615,993 bytes.

Current Codex baseline on the same local history:

- `bun run scriba codex daily --json`: 8.77s real, 614,727,680 bytes max RSS.
- `bunx -p @ccusage/codex@18.0.11 ccusage-codex daily --json`: 24.73s real,
  6,893,535,232 bytes max RSS.
- `scriba bench ccusage --provider codex --execute --timeout-ms 30000`: daily
  completed in 19,822ms, monthly timed out at 30,151ms, session completed in
  19,232ms.

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
