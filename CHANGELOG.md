# Changelog — demonware

`git log` stays the source of truth; this file is the readable summary.

## v0.6.0 — 2026-11-04

- **Account erasure (openpak/prds#120).** `DELETE /internal/accounts/{account_id}/data` on the health listener (`:22611`, container network only) with `Authorization: Bearer $ACCOUNT_ERASE_KEY` (unset: 503) deletes every stored ticket (`sessions`) and CTR wallet (`ctr_wallets`) of the account and answers `{"service":"demonware","rows":N}`. The account's switch subjects come from the core (`ACCOUNT_GRPC_ADDR`, default `openpak-account:20001`, `ACCOUNT_INTERNAL_KEY`); PIDs from the stored sessions and from nx-baas. Idempotent; logs one `account erased` line. No `account_deleted` consumer (the service has none).
- Kept: nothing per player. Diablo III is publisher settings only; the lobby, NAT and relay hold only transient state.

## v0.5.1 - 2026-11-04

- Docs: unsupported methods listed so the titles page promises nothing it does not do (openpak/prds#113).

## v0.5.0 — 2026-11-03
- Per-request logging: one slog line per auth HTTP request (method, path, status, ms) via internal/httplog and one 'lobby task' line per lobby task (service, task, ms). Query strings, headers and bodies are never logged; slog default now set to the daemon logger.

## v0.4.0 — 2026-11-03

- **CTR wallets live in Postgres** (`ctr_wallets`, one row per account; Wumpa Coins, owned items, receipts). The old `ctr-wallets/<pid>.json` files are imported once at start (existing rows win) and left in place.
- The service now refuses to start without `DEMONWARE_DATABASE_URL` (local runs: `DEMONWARE_ALLOW_FILE_STORE=1`). `diablo3.json` is publisher configuration, not player state, and stays a file.

## v0.3.0 — 2026-11-03

- **Tickets live in Postgres** (`DEMONWARE_DATABASE_URL`, pool of 3). The in-memory map is only a cache; `Find` falls back to the table, so a restart loses no ticket. The old `sessions.json` is imported once. Restart test: `TEST_DATABASE_URL`.

## v0.2.1 — 2026-11-03

- Docs: why this host sends no rewards session events (joins are never observed by the server).

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
- Supersedes the `platforms/switch/games/crash-team-racing` feasibility study (`docs/crash-team-racing.md`).
