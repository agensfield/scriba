# Offline differential receipts

These receipts are bounded, sanitized source projections, not captured terminal UI. They pin the peer repository commit, version, arguments, timezone, normalized pricing semantics, Scriba's canonical expected projection, and any intentional semantic waiver.

CI reads checked-in JSON only. Refreshing a receipt is a deliberate offline maintenance task against the pinned peer sources; tests never execute peer package managers or access the network.

`codex-gpt-5.6.receipt.json` covers the Cartesian product of GPT-5.6 Sol/Terra/Luna and 271999/272000/272001 input tokens, with one output token and no cached input. ccusage agrees with Scriba's strict `> 272000` whole-request tier switch. OpenUsage has flat rates in the pinned snapshot, so equality above the boundary is explicitly waived.
