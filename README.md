# demonware

OpenPak's Demonware server: auth3, the encrypted lobby and the remote tasks that Activision's
Demonware titles call on Nintendo Switch. One module, one image (`ghcr.io/openpak/demonware`),
one process for every title.

| Title | `DEMONWARE_TITLES` | Demonware id | Status |
| --- | --- | --- | --- |
| Diablo III: Eternal Collection | `diablo-3` | 5745 ("crimson") | two Harbor consoles: Quick Match joins a public game (2026-10-10) |
| Crash Team Racing Nitro-Fueled | `crash-team-racing` | 5775 ("lavender") | two Harbor consoles race via matchmaking (2026-10-10); login needs the CTR key patch (below) |

Built clean-room from the facts in [`docs/protocol.md`](docs/protocol.md); where every fact came
from is in [`docs/provenance.md`](docs/provenance.md). This repository supersedes the
`servers/crash-team-racing` feasibility study, whose content and open questions are carried in
[`docs/crash-team-racing.md`](docs/crash-team-racing.md).

## Why one process for both titles

The games dial the lobby on TCP 3074 and their NAT probes on UDP 3074, fixed in the client, and
both resolve to the same OpenPak address. Only one process can own those ports, and a lobby
connection only says which game it is after its ticket is read. So the title comes from the
ticket (auth3 stamps it in), and `DEMONWARE_TITLES` only chooses which games auth3 admits.

## What works

Both titles:

- **auth3** (`POST /auth/`): proves the player's NSA id_token through nx-baas
  (`/internal/switch/identity`, the `openpak` claim or the whole token), refuses banned (403
  `account_banned`) and unknown accounts, and fails closed when nx-baas cannot answer. The
  ticket carries a stable per-account id: the network service account id nx-baas holds for the
  player, which is also the id the Switch friend service gives the game for that player.
- **Lobby**: hello, ticket-signed handshake, AES/HMAC channel, keepalives, FIFO task replies.
  Only a ticket this server issued and has not expired opens a session: no all-zero key, no
  key taken from an unknown ticket. Sessions last `DEMONWARE_SESSION_TTL` and survive a restart
  (`$DEMONWARE_DATA/sessions.json`). A banned account is disconnected within a minute.
- **NAT**: IP and NAT discovery on UDP 3074; introductions between connected players only.
- **Shared tasks**: server time (12/6), friend names (12/9, friends online in the same game),
  user data (29/1, 29/4), rich presence (68/3/4/5/7; a player sets only their own), A/B
  testing (194/1).
- Any task nobody implements gets an empty success, so the game never waits on a reply.

Diablo III:

- Publisher files `Config.txt`, `Seasons.txt`, `Blacklist.txt` (10/21), from
  `DEMONWARE_D3_SETTINGS` (JSON, read on every request; defaults: season 37 with its theme).
- Public games (21/1, 2, 3, 5, 12, 19, 20): create, update, player counts, delete, find. Full
  games and games whose host has gone are not offered; a game with no host for 2 minutes, or
  untouched for 6 hours, is removed.
- Empty successes for leaderboards (4), mail (6), hero upload (10/10...), counters (23), DML
  (27), the rest of 29, event log (67): nobody has decoded those formats yet.

Crash Team Racing:

- Umbrella token `POST /v1.0/tokens/lsg/` for a live ticket issued here.
- Signed auth replies (`X-Signature`, RSA-PSS SHA-256, zero salt) when
  `DEMONWARE_CTR_SIGNING_KEY` is set.
- Friend sessions and contextual games (138): friends show as joinable.
- Async matchmaking (145): searches (parties included) grouped by identical filter, host
  chosen, 2005/2006/2001 pushes, host-doc sync with version check, seat release for guests the
  host reports as timed out, backfill of open lobbies. Lobby merges (2008) are not implemented.
- Pit Stop (80): balances (245), inventory (165), purchases with bundle pricing (154), eShop
  reconciliation acknowledged (246). Wallets persist per OpenPak account under
  `$DEMONWARE_DATA/ctr-wallets/`.
- Achievements engine (125): race rewards from the game's own table with its online, weekend
  and Golden Wumpa rules, paid once per race; Pit Stop daily refresh (two a day); the Window
  Shopping challenge (25 coins). Other challenges are not implemented; leaderboards (4) are
  empty.
- Optional UDP relay for pairs whose NAT hole punch fails (`DEMONWARE_RELAY_PORTS`), per
  player allow-list.

Not done, for either title: server-reported presence (the console reports its own through
nx-baas; there is no game-server presence intake), leaderboards, hero storage, mail, Challenge
Rifts, season rotation, search filters for Diablo III.

## Environment

| Variable | Default | Meaning |
| --- | --- | --- |
| `DEMONWARE_TITLES` | `diablo-3,crash-team-racing` | games auth3 admits |
| `DEMONWARE_PUBLIC_IP` | (required) | IPv4 consoles reach this server on; announced in NAT replies and relay slots |
| `AUTH_LISTEN` | `:22610` | auth3, TLS terminated here (Traefik passes 443 through by SNI) |
| `CERT_FILE`, `KEY_FILE` | `demonware.pem`, `demonware.key` | certificate for the auth3 names, from the OpenPak CA |
| `DEMONWARE_PROXY_PROTOCOL` | off | `1`: every auth connection starts with a PROXY v1 header (Traefik `proxyProtocol: {version: 1}`) |
| `LOBBY_LISTEN` | `:3074` | lobby, comma list |
| `NAT_LISTEN` | `:3074` | UDP NAT probes and introductions |
| `HEALTH_LISTEN` | `:22611` | `GET /health` |
| `NX_INTERNAL_URL` | `http://127.0.0.1:20070` | nx-baas internal API |
| `NX_INTERNAL_KEY` | (required) | nx-baas internal key |
| `DEMONWARE_REQUIRE_ACCOUNT` | `1` | `0` admits players nx-baas cannot prove, for local tests only |
| `DEMONWARE_DATA` | `data` (`/app/data` in the image) | sessions, CTR wallets, Diablo III settings |
| `DEMONWARE_SESSION_TTL` | `24h` | ticket lifetime |
| `DEMONWARE_D3_SETTINGS` | `$DEMONWARE_DATA/diablo3.json` | Diablo III publisher settings (see below) |
| `DEMONWARE_CTR_START_BALANCE` | `0` | Wumpa Coins a new CTR wallet opens with |
| `DEMONWARE_CTR_SIGNING_KEY` | unset | RSA-2048 PEM key that signs CTR auth replies |
| `DEMONWARE_RELAY_PORTS` | unset | e.g. `22612-22619`: enables the CTR/D3 UDP relay |
| `DEMONWARE_RELAY_FOR` | empty | `*` or a comma list of online ids that get relay offers |
| `LOG_LEVEL` | info | `debug` logs every task |

Diablo III settings file (every field optional):

```json
{ "season": 37, "theme": true, "events": ["DoubleGoblins"], "xp": "2.0",
  "gold_find": "1.0", "legendary_find": "1.0",
  "season_start": "Wed, 01 Jan 2020 00:00:00 GMT", "season_end": "Tue, 01 Jan 2036 00:00:00 GMT" }
```

Dates need a two-digit day and are clamped to 1 Jan 2038. **Changing the served season, up or
down, has broken seasonal heroes on a console's save**: pick one and keep it.

## Ports

Block 22610-22619 in [`ports.md`](../../ports.md):

| Port | What | Reached how |
| --- | --- | --- |
| 22610/tcp | auth3 (TLS) | Traefik `HostSNI` passthrough from 443 |
| 22611/tcp | `/health` | local |
| 22612-22619/udp | optional relay, one port per player pair | published, only when `DEMONWARE_RELAY_PORTS` is set |
| 3074/tcp | lobby | direct, fixed by the games: published and open in the firewall |
| 3074/udp | NAT probes and introductions | direct, fixed by the games |

NAT discovery must see the console's real address and port, so the container runs with host
networking (like `switch-nexd`), not a published port behind rootless port forwarding.

## DNS

`nn-sssl-dns` must answer these with the production address:

```
crimson-switch-auth3.prod.demonware.net   crimson-switch-lobby.prod.demonware.net
crimson-switch-auth3.cert.demonware.net   crimson-switch-lobby.cert.demonware.net
crimson-switch-auth3.dev.demonware.net    crimson-switch-lobby.dev.demonware.net
lavender-switch-auth3.prod.demonware.net  lavender-switch-lobby.prod.demonware.net
prod.umbrella.demonware.net               (CTR's LSG token, served by auth3)
stun.us.demonware.net  stun.eu.demonware.net  stun.jp.demonware.net  stun.au.demonware.net
```

Without the `stun.*` names the game probes Activision and only offers local games.

## Deploy notes

- **Traefik**: one `HostSNI` router per auth3 name above and `prod.umbrella.demonware.net`
  (`tls.passthrough: true`) to a service
  at `host.containers.internal:22610`. Optionally `proxyProtocol: {version: 1}` on that service
  together with `DEMONWARE_PROXY_PROTOCOL=1`, so auth logs show the console's address; the
  current OpenPak Traefik config sends no PROXY header, so the default is off.
- **Certificate**: an OpenPak-CA leaf for the auth3 names and `prod.umbrella.demonware.net`
  (the prod leaf since 2026-10-10 also names `cert.umbrella`, `prod.uno` and `cert.uno`); TLS 1.0 and up is accepted
  because the games' HTTP client is old.
- **Quadlet**: `openpak-demonware.container`, `Image=ghcr.io/openpak/demonware:latest`,
  `Network=host`, a volume on `/app/data`, the certificate mounted read-only, the shared
  `.env` for `NX_INTERNAL_KEY`.
- **Firewall**: ACCEPT 3074/tcp and 3074/udp (and 22612-22619/udp if the relay is on) before the
  host's final REJECT.
- **CTR login needs the CTR key patch.** CTR checks the auth reply's `X-Signature` against the
  key embedded in its `main`. [`tools/ctr-key-patch`](tools/ctr-key-patch) swaps that key for
  OpenPak's (a data-only IPS for the final update); `openpak.nro` installs it on consoles and
  Ryujinx, Citron and Eden apply it built in. Deploy the matching private key as
  `DEMONWARE_CTR_SIGNING_KEY`; the lobby then also accepts a key wrapped with it.

## Run locally

```sh
DEMONWARE_PUBLIC_IP=127.0.0.1 DEMONWARE_REQUIRE_ACCOUNT=0 CERT_FILE=… KEY_FILE=… go run ./cmd/demonwared
```

Tests on this shared machine: `flock /tmp/openpak-build.lock nice -n 19 go test ./...`.

This project is not affiliated with or endorsed by Nintendo, Activision, Blizzard, Beenox or
Demonware. It contains no game code, assets, keys or captures. Licence: AGPL-3.0-only.
