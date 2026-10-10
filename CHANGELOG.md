# Changelog — demonware

`git log` stays the source of truth; this file is the readable summary.

## v0.1.5 — 2026-10-10

- CTR async matchmaking logs each step at info level: pushes sent (event only), lobby document
  reads, host syncs (accepted, or why not: missing key, not the host, stale version), searches
  cancelled, lobbies left. First two-console test: both consoles reach Online and search, the
  host is sent 2005, nobody joins; these lines show where it stops.

## v0.1.4 — 2026-10-10

- Diablo III Quick Match (21/5) returns only the public games the search asks for: mode,
  difficulty, version, region, option and flag equal, level in the searcher's range, room for
  the party, act and quest as the query id says; closest level first. Layouts read from the
  v1441792 `main` (docs/protocol.md §6). Search lines log open/returned counts and why games
  were left out.

## v0.1.2, v0.1.3 — 2026-10-10

- CTR's LSG token comes from `prod.umbrella.demonware.net` (its own host); deployment now routes
  and certifies that name. auth logs paths it does not serve (shape only, via `redact.Path`).
- Matchmaking logs game attributes and search filters by type and value (strings and blobs by
  length, ids masked), with a log-privacy test.

## v0.1.1 — 2026-10-10

- ci: build scratch in the runner temp, not the checkout.
- Logs no longer carry pids, account or NSA ids, nicknames, tokens, tickets, pairing or search ids,
  or client addresses. Where two lines have to be matched they carry a short per-process tag
  (`redact.Tag`: keyed HMAC prefix, key random per start). Lobby task bodies are logged only with
  `DEMONWARE_TRACE_BODIES=1`. Net and URL errors drop the addresses they spell out.
- `TestLogsCarryNoPersonalData` in auth, lobby and titles/ctr fails if a known id, name or token
  reaches a log line.

## v0.1.0 — 2026-09-23

First version. Deployed to prod.

- CTR key patch (`tools/ctr-key-patch`): a data-only IPS for the final update swapping the
  embedded RSA modulus for OpenPak's, the one game patch OpenPak ships; the lobby accepts a
  lobby key wrapped with the configured signing key's public half.

- auth3 for Diablo III (5745) and Crash Team Racing Nitro-Fueled (5775): identity through
  nx-baas `/internal/switch/identity`, banned and unknown accounts refused, fail-closed;
  optional PROXY protocol v1; CTR umbrella token and signed replies.
- Lobby: hello, ticket-signed handshake, encrypted channel, task dispatch. Only tickets issued
  here open a session (no all-zero key); sessions expire and persist across restarts; banned
  accounts are disconnected within a minute.
- NAT discovery and introductions on UDP 3074 (introductions only between connected players);
  optional per-pair UDP relay.
- Shared tasks: server time, friend names, user data, rich presence, A/B testing,
  session matchmaking (21 and 138, with expiry).
- Diablo III publisher files from a JSON settings file.
- Crash Team Racing async matchmaking, Pit Stop with persistent wallets, race rewards, Pit Stop
  refresh, Window Shopping.
- Supersedes the `servers/crash-team-racing` feasibility study (`docs/crash-team-racing.md`).
