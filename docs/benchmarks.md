# Scriba Benchmarks

The Go migration reset the benchmark baseline. Re-run the comparison after the
new CLI has settled.

Useful commands:

```sh
scriba cache reset
/usr/bin/time -l scriba codex daily --json
/usr/bin/time -l scriba codex daily --json
scriba bench ccusage --provider codex --json
```

The historical TS/Bun evidence remains in the vault under the Scriba living
spec and session log.
