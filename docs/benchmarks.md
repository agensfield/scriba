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

- `bun run scriba codex daily --json` cold after `scriba cache reset`: 6.98s
  real, 626,802,688 bytes max RSS.
- `bun run scriba codex daily --json` warm with file-event cache: 0.24s real,
  156,205,056 bytes max RSS.
- `bunx -p @ccusage/codex@18.0.11 ccusage-codex daily --json`: 24.73s real,
  6,893,535,232 bytes max RSS.
- `scriba bench ccusage --provider codex --execute --timeout-ms 30000`: daily
  completed in 19,822ms, monthly timed out at 30,151ms, session completed in
  19,232ms.

Current cache-backed status/report evidence after the Claude cache parity pass:

- `bun run scriba status --json --no-cache`: 8.50s real, 1,023,508,480 bytes
  max RSS.
- `bun run scriba status --json` cold after `scriba cache reset`: 8.94s real,
  992,968,704 bytes max RSS. This populates both Claude and Codex parsed
  file-event caches.
- `bun run scriba status --json` warm immediately after that cold run: 1.00s
  real, 218,202,112 bytes max RSS.
- `bun run scriba claude daily --json --no-cache`: 1.94s real, 570,638,336
  bytes max RSS.
- `bun run scriba claude daily --json` warm: 0.20s real, 121,667,584 bytes max
  RSS.
- `bun run scriba codex daily --json --no-cache`: 6.22s real, 753,238,016
  bytes max RSS.
- `bun run scriba codex daily --json` warm: 0.31s real, 180,912,128 bytes max
  RSS.

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
