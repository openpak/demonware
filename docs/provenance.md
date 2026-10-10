# Provenance

Where each non-obvious fact in `docs/protocol.md` came from, per the
[clean-room policy](../../../docs/clean-room-policy.md).

**Fact source:** `earlier/legacy-demonware` (source-available), read on
2026-09-23 from a clone outside this repository, for facts only. Its README, STATUS.md,
NOTES.md, PUBFILES.md, SEASONS.md and Go sources were read; nothing was copied, translated
or mirrored. Its file layout, package split, type and function names, and table shapes are
not used here. Its own sources credit the reverse engineering of Diablo III's binary to
nx-mod and the Crash Team Racing work to CollectingW.

"NX:<file>" below means that file in the fact source.

| Fact | Source |
| --- | --- |
| D3 Demonware title id 5745, "crimson", Switch title `01001B300B9BE000` | NX:NOTES.md §header, NX:README.md |
| D3 hosts `crimson-switch-{auth3,lobby}.{prod,cert,dev}.demonware.net`, `stun.{us,eu,jp,au}.demonware.net` | NX:NOTES.md §1 ("from rodata"), NX:README.md hosts entries |
| CTR title 5775, CTR Switch app id `0100f9f00c696000` | NX:README.md, NX:presence.go |
| CTR hosts `lavender-switch-{auth3,lobby}.prod.demonware.net` | OpenPak's own tree: `emulators/citron/src/core/hle/service/sockets/sfdnsres.cpp` (observed live 2026-08-31), `servers/crash-team-racing/docs/design.md` |
| auth3 on 443 via TLS, URL `https://%s:%d/auth/`, old curl needs TLS 1.0 | NX:auth.go, NX:NOTES.md §2 |
| Lobby on TCP 3074 (neighbouring ports 3075-3080 opened "because the real port can't be deduced") | NX:lobby.go, NX:NOTES.md §1 |
| Auth request fields, string integers, extra_data contents | NX:NOTES.md §2, NX:identity.go |
| Reply field order, auth_task + 1 (78 -> 79 seen), code 700, 735 catch-all | NX:NOTES.md §2, NX:auth.go |
| `nso_subscription_status` required; `extended_data` required non-empty <= 4096 | NX:auth.go |
| Ticket layout and magic 0xEFBDADDE, clear-read when magic present | NX:NOTES.md §2, NX:auth.go |
| CTR online id at ticket +25, must be stable | NX:auth.go, NX:README.md §Crash Team Racing |
| CTR `X-Signature`, RSA-PSS SHA-256 zero salt, client mod needed | NX:README.md, NX:ctr_signing.go |
| CTR's verify key is the embedded Demonware SPKI at NSO `0x40B4E40`; the X-Signature handler (mapped `0xd4e0`..`0xd5a0`) and the lobby key wrap (`0x14584`, flag-gated) both load it | OpenPak's own dump of the v983040 update, disassembled (`tools/ctr-key-patch/README.md`) |
| Umbrella `/v1.0/tokens/lsg/` request and reply fields | NX:ctr_umbrella.go |
| Umbrella host `https://{prod,cert,dev}.umbrella.demonware.net`, URL `%s/v1.0/tokens/lsg/?client=%s`; lobby port 3074 and title 5775 in the same config block as the lavender host names | OpenPak's own dump of the v983040 `main` (strings and their loads at mapped `0x10e2c8`, `0x10eeb4`, `0xa1f72c`) |
| Hello layout, frame layout, 0x81..0x85, keepalive echo | NX:NOTES.md §3, NX:handshake.go |
| Transcript, HMAC/HKDF-style expand, CLIENTCHAL/BDDATA split | NX:NOTES.md §3, NX:bdcrypto.go |
| Optional RSA-DER wrapping of the lobby key and the 294-byte DER itself (a constant in the game binary) | NX:NOTES.md §3, NX:pubkey.go |
| 0x85 layout, seq from 1, tag = HMAC-SHA1(...)[:8] | NX:NOTES.md §3 |
| 0x87 migration unnecessary, drops after ~12 s | NX:handshake.go, NX:README.md |
| Typed buffer tags, array encoding, 0x14 sentinel | NX:NOTES.md §4, NX:services.go, NX:friends.go, NX:ctr_marketplace.go |
| Task request/reply layout, FIFO matching, total only when count > 0, error semantics | NX:NOTES.md §4, NX:services.go |
| Unanswered task fails the login sequence | NX:services.go |
| Struct replies have no count fields (error 4 otherwise) | NX:services.go, NX:ctr_marketplace.go |
| Push = inner 0x02 with u32 event type | NX:services.go |
| NAT probe formats 30/31, 20/21, second probe `14 02 00 03`, type 14 keepalive | NX:NOTES.md §5, NX:nat.go (which credits project-bo4/shield-development) |
| 29-byte introduction layout, 0x0A -> 0x0B unchanged, host replies to in-packet src | NX:nat.go (which credits Protarium-Network/bo2-wiiu-demonware) |
| bdCommonAddr 67-byte slot layout, relay slots used by the game | NX:ctr_relay.go |
| 12/6, 12/9, 29/1, 29/4 layouts; friend ids are the friend service's ids | NX:friends.go, NX:NOTES.md §4 and "Friends: scope" |
| 68 rich presence layouts | NX:ctr_presence.go, NX:richpresence_test.go |
| 194/1 A/B testing reply, status in fields 1 and 2, non-empty ABToken | NX:services.go |
| Service 21 task numbers and layouts, observed join sequence, stale-game failure | NX:matchmaking.go, NX:NOTES.md §header and §7 |
| Service 138 context, CTR attribute blob, 138/14 friend sessions | NX:services.go, NX:matchmaking.go |
| D3 public-game attribute and Quick Match filter layouts, query ids 1-3, the level range formula, max 20 results | OpenPak's own dump of the v1441792 `main`: the 21/5 builder at mapped `0xbe2120`, its caller `0x8a680` (query choice, filter values), the filter writer `0x8abd0` and the game-info writer `0x8acc0` with its setter `0x89ad4` |
| D3 empty-success services list | NX:STATUS.md, NX:NOTES.md §4 |
| Publisher file names, `update-1.cpk` absent, rift file names | NX:NOTES.md §4, NX:riftdata.go |
| Config.txt keys and order, 21 event names, Seasons.txt and Blacklist.txt shapes, date format, 2038 limit | NX:pubfiles.go, NX:PUBFILES.md |
| Season themes 14-39 | NX:SEASONS.md, NX:pubfiles.json (which cite d3hack's season map) |
| Season 37 tested; season changes damage saves | NX:PUBFILES.md, NX:STATUS.md |
| Buff window defaults 2023-09-16 .. 2027-12-01, season window 2020 .. 2036 | NX:pubfiles.json |
| 145 task numbers and result types, player info and search doc fields | NX:services.go, NX:ctr_async.go |
| Host doc field order and meaning, player states 1/2/3 | NX:lobbydoc.go, NX:ctr_async.go |
| Backend doc fields, push events 2001/2005/2006/2008 and their id lists | NX:ctr_async.go, NX:ctr_merge.go |
| Sync version rule, required sync keys, closed lobby takes no players | NX:ctr_async.go |
| Marketplace service 80, tasks 245/246/165/154, request and reply layouts, currencies 10-12 | NX:ctr_marketplace.go, NX:ctr_marketplace_test.go |
| Zigzag amounts | NX:ctr_marketplace.go |
| SKU catalog (items, parts, full price) | NX:ctr-skus.json (extracted from the game's catalog; restated as `data/skus.tsv`) |
| Bundle price = unowned parts | NX:ctr_economy.go |
| Push 76 balance layout | NX:ctr_rewards.go |
| Service 125 tasks, struct replies, push 75, state encoding, request fields | NX:ctr_achievements.go, NX:ctr_refresh.go |
| Pit Stop refresh parameters (id 255, 15:00 UTC, target 2) | NX:ctr_refresh.go (cites the game's AchievementMetadata) |
| Window Shopping parameters | NX:ctr-window-shopping.json (cites the game's AchievementMetadata) |
| Race reward table and multipliers | NX:ctr-race-rates.json, NX:ctr_rewards.go (cite the game's map table and CNetworkConfig) ; restated as `data/race-rewards.tsv` |
| CTR leaderboards unimplemented | NX:STATUS.md |

## Decisions of our own (not facts about the game)

- Tickets are looked up by an id we choose inside the ticket and must match what we issued
  byte for byte; the all-zero key and keys carried in an unknown ticket are never tried.
- Demonware user id = the player's network service account id from nx-baas (see
  `docs/protocol.md` §9).
- One process for both titles: the lobby and NAT ports are fixed by the games and shared.

## Unexplained or contradictory

- **CTR lobby port.** Our citron hook rewrites destination port 443 for *both*
  `lavender-switch-auth3` and `lavender-switch-lobby`, which suggests the CTR lobby might
  be dialled on 443. The fact source serves CTR's lobby on 3074 only (the lobby is not TLS,
  so it cannot share 443 behind an SNI router) and reports CTR working. Not resolved; we
  serve 3074 (`LOBBY_LISTEN` accepts extra ports).
- **Which key the CTR client uses** for the lobby (plain lobby key, or the RSA-wrapped one).
  Both are tried.
- **Client-to-server 0x85 layout** was assumed by the fact source to mirror the
  server-to-client one; it works in practice but was never read in the binary.
- **Friend ids on a console.** The fact source saw console friends identified by device or
  network service account ids and Citron friends by earlier PIDs. On OpenPak both consoles
  and emulators get their friend list from nx-baas, whose friend id is the network service
  account id. We key on it; a PID match is kept as a fallback.
- **Config.txt keys beyond these**, the blacklist `0`/`1` meaning, `ParagonCap`, and whether
  `SeasonOnly "0"` is safe are open in the fact source.
- **Seasons.txt comment lines.** The fact source writes two `#` comment lines first; we omit
  them. Assumed harmless, not tested on a game.
- **Quick-match refusal** when one attribute differs (flag 1 vs 0): the last filter value is compared with the last attribute (docs/protocol.md §6); which setting it is was not traced.
- **Race rewards** are keyed by the game's track-name hash; the hash function itself is not
  known to us, only the table.
