# Scriba Benchmarks

The Go migration reset the performance baseline. Token and cost parity was
rechecked against ccusage v20.0.17 during the GPT-5.6 accounting pass.

Stable UTC evidence for 2026-07-09:

| Metric | Scriba | ccusage |
| --- | ---: | ---: |
| Uncached input | 21,804,496 | 21,804,496 |
| Cached input | 351,767,936 | 351,767,936 |
| Output | 1,009,972 | 1,009,972 |
| Full traffic | 374,582,404 | 374,582,404 |
| Standard API-equivalent cost | $297.981505 | $297.981505 |

The same comparison in `Europe/Istanbul` produced exact component, total, model,
and cost parity for the local 2026-07-09 bucket. Pin the timezone when comparing
calendar reports; ccusage and Scriba both otherwise default to local time.

Useful commands:

```sh
scriba cache reset
/usr/bin/time -l scriba codex daily --json
/usr/bin/time -l scriba codex daily --json
scriba bench ccusage --provider codex --json
scriba codex daily --since 2026-07-09 --until 2026-07-09 --timezone UTC --no-cache --json
```

The historical TS/Bun evidence remains in the vault under the Scriba living
spec and session log.
