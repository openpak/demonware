# Demonware on Switch: protocol notes

The facts this server is built from, written down before any code. Every entry says what the
game sends or expects; `docs/provenance.md` says where each fact came from. Nothing here is a
description of another implementation's code.

Integers are little-endian unless stated. "Typed" means the Demonware typed byte buffer
described in section 4.

## 1. Titles and hosts

| Title | Demonware title id | Switch title id | Hosts the game resolves |
| --- | --- | --- | --- |
| Diablo III: Eternal Collection | 5745 ("crimson") | `01001B300B9BE000` | `crimson-switch-auth3.{prod,cert,dev}.demonware.net`, `crimson-switch-lobby.{prod,cert,dev}.demonware.net` |
| Crash Team Racing Nitro-Fueled | 5775 ("lavender") | `0100F9F00C696000` | `lavender-switch-auth3.prod.demonware.net`, `lavender-switch-lobby.prod.demonware.net` |
| both | | | `stun.{us,eu,jp,au}.demonware.net` (NAT probes) |

Ports the game dials:

- auth3: HTTPS on **443**, URL template `https://<host>:<port>/auth/`. The client is an old
  embedded curl, so the TLS server must accept TLS 1.0 and up.
- lobby: plain TCP (no TLS) on **3074**. The lobby host name is only used for DNS.
- NAT probes and introductions: UDP **3074**, sent from the client's own UDP 3074.

The auth reply does not carry the lobby address; the client has it built in.

## 2. auth3

`POST /auth/`, JSON body. Every integer in the request is a JSON *string*.

```
auth_task   "78"            request number; the reply must carry this + 1
iv_seed     "<n>"           echoed back
title_id    "5745" | "5775"
identity    "356c4bc3"      opaque
extra_data  "<json string>" version, token (the NSA id_token), username, extended_data
```

Reply, HTTP 200, `Content-Type: application/json`. **Field order matters**: the client
parses sequentially in this order.

```
auth_task      request + 1, as a string
code           "700"        (any other value fails; 735 is the client's catch-all failure)
iv_seed        echoed
client_ticket  standard base64 (+/, padded) of exactly 128 bytes
server_ticket  standard base64 of exactly 128 bytes; opaque to the client, echoed to the lobby
extra_data     a string holding JSON:
               nso_subscription_status  "1"   required, read as u16; without it every reply is 735
               extended_data            non-empty, at most 4096 chars; without it the login
                                        sequence fails later and the game goes offline
```

Refusing a player: an HTTP error status with no ticket (403 is what the game has been seen to
tolerate as "cannot log in").

### Ticket (128 bytes)

```
+0    u32  magic 0xEFBDADDE (bytes DE AD BD EF). With the magic present the client reads the
           ticket in the clear (no decryption key needed).
+4    u8   (1)
+5    u32  Demonware title id
+9    u32  (0)
+13   u32  issue time, unix seconds
+17   u64  user id
+25   u64  Diablo III: expiry. Crash Team Racing reads its *online id* here, and it must be
           stable per player: the game ties a match to its host by comparing this id with its
           friends' online ids.
+33   [64] session key
+97   [24] lobby key: signs the lobby handshake
+121  [7]  unknown
```

Putting the first 24 bytes of the session key at +97 as well keeps both possible reads of the
lobby key equal.

### Crash Team Racing extras

- **Signed replies.** CTR checks an `X-Signature` header on the auth reply before reading the
  ticket: base64 of an RSA-PSS / SHA-256 signature with a **zero-length salt** over the exact
  response body, RSA-2048, exponent 65537. The client verifies with the key embedded in its `main`
  (Demonware's, the same one `lobby/crypto.go` names), so signed replies need
  [`tools/ctr-key-patch`](../tools/ctr-key-patch), which swaps it for OpenPak's. The lobby
  key wrap uses the same embedded key, so a patched client wraps with OpenPak's.
- **Umbrella token.** `POST /v1.0/tokens/lsg/?client=<name>` on
  `https://prod.umbrella.demonware.net` (its own host, not auth3; `cert.` and `dev.` variants
  exist), JSON
  `{"ticket": "<base64 128 bytes>", "titleID": 5775 or "5775"}`. Reply JSON
  `{"umbrellaID": <u64>, "accessToken": "<string>", "expires": <unix s>, "accounts": []}`.

## 3. Lobby transport

### Hello (client, raw, 28 bytes, unframed)

```
u32 200 | u32 200 | u32 210 | u32 220 | u32 maxFrame (0x1FFFFF seen) | nonce[8]
```

### Frames (both directions after the hello)

```
u32 L | u8 0xAB | body[L-1]          body[0] is the message type; L = 0 is a keepalive
```

A keepalive is answered with a keepalive (4 zero bytes).

### Handshake

1. server `0x81`: `u32 version (210..220; 220 used) | u64 connection id | 8 bytes the client skips`.
   The u64 is later surfaced to the game as the connected user id.
2. client `0x82`: a *bit* buffer holding `{u32, u32, 128-byte server ticket}`, followed by an
   8-byte tag. The fields before the ticket are not byte-aligned; the ticket is found by its
   magic `DE AD BD EF` at some bit offset (either bit order).
3. server `0x83`: `check[8]` on success, or `0x84`: `u32 error`.
4. everything after is `0x85` (encrypted).

### Key derivation (SHA-1 throughout)

```
transcript = u32 210 | u32 220 | u32 maxFrame | nonce[8]
           | u32 (len(0x81 payload) + 2) | 0xAB | 0x81 | 0x81 payload (20 bytes)
           | the whole 0x82 frame, length prefix included, minus its last 8 bytes
secret     = HMAC-SHA1(key = SHA1(transcript), message = lobbyKey24)
expand(k, label, n): T1 = HMAC(k, label | 0x01), Ti = HMAC(k, T(i-1) | label | i), concatenated, cut to n
CLIENTCHAL = expand(secret, "CLIENTCHAL", 16)   [0:8] = the 0x82 tag, [8:16] = the 0x83 check
BDDATA     = expand(secret, "BDDATA", 72)
             [0:20]  client->server HMAC key
             [20:40] server->client HMAC key
             [40:56] client->server AES-128 key
             [56:72] server->client AES-128 key
```

A client flag can replace `lobbyKey24` by `expand(lobbyKey24, <the game's embedded RSA-2048
public key, 294-byte DER SubjectPublicKeyInfo>, 24)`. Diablo III does not set it; whether
CTR does is not known, so a server should try both.

### Encrypted message `0x85`

```
u32 seq (starts at 1) | IV[16] | AES-128-CBC( u32 N | u8 innerType | payload[N] | zero pad to 16 ) | tag[8]
tag = HMAC-SHA1(direction MAC key, the whole frame from the length prefix up to the tag)[:8]
```

The frame length counts the tag.

### Inner message types

| type | direction | meaning |
| --- | --- | --- |
| `0x86` | client -> server | remote task request |
| `0x01` | server -> client | task reply |
| `0x02` | server -> client | push message (`u32` event type, then payload) |
| `0x87` / `0x88` / `0x89` | | lobby migration. The 0x83 alone completes login; sending 0x87 makes the game wait for a migration and drop after about 12 s. Not needed. |

## 4. Typed byte buffer

| tag | type |
| --- | --- |
| `0x01` | bool (1 byte) |
| `0x03` | u8 |
| `0x06` | u16 |
| `0x07` | i32 |
| `0x08` | u32 |
| `0x09` | i64 |
| `0x0A` | u64 |
| `0x10` | NUL-terminated string |
| `0x13` | blob: `0x13`, then a typed u32 length (`08 u32`), then the bytes |
| `0x17` | struct buffer (protobuf): same framing as a blob |
| `100 + t` | array of t: `tag`, typed u32 byte count, raw u32 element count, raw elements (seen for u64: `0x6E`) |

`0x14` is not an 8-byte integer tag (it is an INT64_MAX sentinel in the i64 writer).

Signed protobuf amounts in struct buffers are **zigzag**-encoded (a plain varint is read back
halved).

### Task request (inner `0x86`)

```
u8 service (raw, no tag) | 03 u8 task | typed arguments | 00
```

### Task reply (inner `0x01`)

```
0A u64 transaction | 08 u32 error | 03 u8 task | 08 u32 numResults | [08 u32 total | results...]
```

- error 0 = results follow; 200 = still pending; anything else fails the task cleanly.
- `total` is only present when numResults > 0.
- With a non-zero error nothing follows the error.
- Replies are matched to requests in FIFO order: the oldest pending task takes the next reply.
- A task that never gets a reply times out and fails the whole login sequence, so every
  request must be answered. An empty success is the safe default for list-type tasks.
- **Struct tasks** (bdStructBuffer tasks) read the struct straight after the task byte:
  `0A u64 | 08 u32 0 | 03 u8 task | 17 08 u32 len | struct bytes`. Result-count fields there
  make the task fail with error 4.

## 5. NAT probes and introductions (UDP 3074)

Not RFC 5389. Raw `type | version | pad` headers:

```
1e 03 00         type 30, IP discovery   -> 1f 02 00 | ip[4] (network order) | port u16 LE
14 02 00 00      type 20, NAT discovery  -> 15 02 00 | ip[4] | port u16 LE | serverIp[4] | serverPort u16 LE
14 02 00 03      a second NAT probe; same answer
```

The observed address is the one the packet came from. Without answers the NAT type stays
unknown and the game only offers local games.

29-byte traversal packets: `type | u16 version | id[10] | hmac[4] | src ip[4] port u16 LE | dst ip[4] port u16 LE`.

- `0x0A`: a joiner asks to be introduced to the host at `dst`. The server forwards the packet
  **unchanged except its first byte, set to `0x0B`**, to `dst`. The HMAC covers the addresses
  and only the requester checks it. The host then answers `0x0C` directly to `src` (the
  address written inside the packet, not the UDP source).
- `0x0E`: keepalive, no reply.
- An empty destination is `0.255.0.255` or port 0.

### bdCommonAddr (67 bytes, the "host address" blob)

```
slot 0     [0:6]    local address, ip[4] + port u16 LE
slots 1-9  [6:60]   relay addresses; an empty slot is 0.255.0.255 port 0
slot 10    [60:66]  public address
[66]                NAT type
```

A relay endpoint written into a free relay slot of the copy a joiner receives is used by the
game (seen live): the relay then forwards between that joiner and the host's public address.

## 6. Remote tasks: shared

| svc/task | request args | reply |
| --- | --- | --- |
| 12/6 server time | none | 1 result: `08 u32` unix seconds |
| 12/9 user names | typed u64s and/or a u64 array (friend ids) | rows `0A u64 id, 10 nickname` for ids that are online |
| 29/1 set user data | `10 context, 13 blob` | none |
| 29/4 get user data | `10 context, [01 bool], u64s` | rows `0A u64 id, 13 blob` |
| 68/3 rich presence set | `10 context, 0A u64 id (0 = self), 10 platform, 03 u8 flag, 13 blob` | none |
| 68/4, 68/5 rich presence get (and subscribe) | `10 context, 08 u32 count, count x (0A u64 id, 10 platform)` | rows `0A u64 id, 10 platform, 03 u8 flag, 13 blob` |
| 68/7 unsubscribe | | none |
| 194/1 A/B testing enroll | | struct reply `{1: http status, 2: http status, 3: body}`; body JSON `{"expiresIn":3600,"ABToken":"<non-empty>","enrollments":[]}`. Status is written in both fields because two readers exist (field 2 and field 1). An empty ABToken fails the login. |

Friend ids: the ids the game asks about are the ones Nintendo's friend service gives it for
each friend (the friend's network service account id on a console). A server answers for
players it knows are connected.

### bdMatchMaking (service 21, Diablo III public games)

| task | request | reply |
| --- | --- | --- |
| 1 create | info | 1 result: `13 blob` session id (8 bytes) |
| 2 update | `13 blob id`, info | none |
| 3 delete | `13 blob id` | none |
| 5 find | `08 u32 query, 08 u32 start, 08 u32 max`, typed filters | rows of session results |
| 12 update players | `13 blob id, 08 u32 numPlayers`, info | none |
| 19 reserve id | none | 1 result: `13 blob` id |
| 20 initialize | `13 blob id`, info | none |

```
info (client)    : 13 blob hostAddr (bdCommonAddr) | 08 u32 gameType | 08 u32 maxPlayers | typed attributes...
result (server)  : 13 blob hostAddr | 13 blob id[8] | 08 u32 gameType | 08 u32 maxPlayers | 08 u32 numPlayers | attributes
```

Diablo III's attributes are the typed bytes after maxPlayers, echoed as sent (the trailing
00 of the request is not part of them). Observed join: host 21/1 then 21/2; joiner 21/5, P2P
connect (with a 0x0A introduction when not on one LAN), then host 21/12 with players=2. A
joiner refuses a found game whose attributes differ from its filter. A game whose host is no
longer in it can still be found, and joining it fails.

#### Diablo III public games and Quick Match

The game's attributes (after maxPlayers), in order:

```
i32 level        compared with the searcher's level range
i32 act          0, 100, 200, 300, 400 = Acts I-V, 3000 = Adventure Mode
i32 quest        -1 = none
13 blob[8], 13 blob[16]
i32 mode         0 normal, 1 hardcore, 2 seasonal, 3 seasonal hardcore, 4 the special game type
u32 free slots
i32 version, i32 region, i32 difficulty, i32 option
i32 (not searched on)
i32 flag         0 or 1
```

Quick Match is 21/5 with up to 20 results and twelve filter values after query, start, max:

```
i32 level, i32 level min, i32 level max     min/max = level -/+ max(2, round(sqrt(level / 2)))
i32 act (-1 any), i32 quest (-1 any), i32 mode, u32 party size,
i32 version, i32 region, i32 difficulty, i32 option, i32 flag
```

The query id says how act and quest are matched: 1 act and quest, 2 act only (Adventure Mode,
or no quest), 3 any act. Every other value must equal the game's, the level must be inside the
range and the game must have room for the party. The server returns only such games, closest
level first; with none the client hosts a new public game itself.

### Contextual sessions (service 138, Crash Team Racing)

Every request starts with `10 context`. Tasks 1, 2, 3, 12, 19, 20 as in service 21, scoped by
context. CTR wraps its attributes in a blob in the request (`... 08 u32 maxPlayers | 13 blob
attrs`, and possibly trailing options outside the blob), while its result reader expects the
blob's *content* directly after numPlayers.

138/14 friend sessions: `10 context, u64s` (friend online ids) -> rows of session results for
games hosted by those friends. Without an answer every online friend shows as not joinable.

## 7. Remote tasks: Diablo III

| svc/task | meaning | reply |
| --- | --- | --- |
| 10/21 getPublisherFile | `10 context, 10 name` | 1 result `13 blob` file bytes, or 0 results for a missing file (normal) |
| 4/1,4,5,11,13 stats/leaderboards | | empty success |
| 6/14, 10/10,12,13, 23/1, 27/2, 29/5,8,11, 67/6 | mail, hero upload, counters, DML, event log | empty success |

Publisher files asked for when a session starts: `Config.txt`, `Seasons.txt`,
`Blacklist.txt`, and `update-1.cpk` (absent is fine). The Challenge Rift menu asks
`challengerift_config.dat` then `challengerift_<n>.dat` (not seen live).

`Config.txt`: one `Key "value"` line per setting:

```
HeroPublishFrequencyMinutes "30"
EnableCrossPlatformSaveMigration "1"
SeasonalGlobalLeaderboardsEnabled "1"
EnableDiablo4Advertisement "0"
CommunityBuffStart "Sat, 16 Sep 2023 00:00:00 GMT"
CommunityBuffEnd "Wed, 01 Dec 2027 01:00:00 GMT"
CommunityBuff<Event> "0"|"1"      for all 21 events, always all present
CommunityBuffLegendaryFind "1.0"
CommunityBuffGoldFind "1.0"
CommunityBuffXP "1.0"
UpdateVersion "1"
```

The 21 events: DoubleGoblins, DoubleBountyBags, RoyalGrandeur, LegacyOfNightmares,
TriunesWill, Pandemonium, KanaiPowers, TrialsOfTempests, SeasonOnly, ShadowClones,
FourthKanaisCubeSlot, EtherealItems, SoulShards, SwarmRifts, SanctifiedItems, DarkAlchemy,
ParagonCap, NestingPortals, EasterEggWorld, DoubleRiftKeystones, DoubleBloodShards.

`Seasons.txt`: `[Season N]`, `Start "<date>"`, `End "<date>"`. Dates are
`Www, DD Mon YYYY hh:mm:ss GMT` with a **two-digit day**. Keep dates before 19 Jan 2038 (the
game appears to hold them in 32 bits).

`Blacklist.txt`: sections `[GBID]` and `[SNO]`, both present even when empty.

Season themes (which events a season switches on): 14 DoubleGoblins; 15 DoubleBountyBags;
16 RoyalGrandeur; 17 LegacyOfNightmares; 18 TriunesWill; 19, 35 Pandemonium; 20, 31, 37
KanaiPowers; 21 TrialsOfTempests; 22, 33, 39 ShadowClones + FourthKanaisCubeSlot; 23 none;
24, 32, 38 EtherealItems; 25, 30, 36 SoulShards; 26 SwarmRifts; 27, 34 SanctifiedItems;
28 DarkAlchemy; 29 NestingPortals. Seasons 1-13 had no theme.

Season 37 is the one tested on game builds 2.7.6 and 2.7.7. Changing the served season, up
or down, has left a console refusing new seasonal heroes until its save was deleted.

## 8. Remote tasks: Crash Team Racing

### Async matchmaking (service 145)

| task | request | reply |
| --- | --- | --- |
| 2 setPlayerInfo | `10 doc` (JSON) | none. First task of registration; failing it breaks the rest. |
| 3 getPlayerToken | | 1 result `0A u64` |
| 4 QoS hosts reply, 14 ack expect game | | none |
| 5, 6, 7, 17 | | 1 result `10 string` |
| 8 cancel search | `0A u64 search id` | 1 result `01 bool` |
| 10 lobby disbanded | `0A u64 lobby id` | none |
| 13 getLobbyDocuments | `0A u64 lobby id` | 1 result `10 host doc, 10 backend doc` |
| 15 syncLobbyDocuments | `0A u64 lobby id, 0A u64 version, 10 host doc` | 1 result `01 bool` accepted |
| 18 startSearch | `10 search doc` | 1 result `10 {"mm_id":<search id>}`, then pushes |

Player info (145/2): `{"listen_server":{"local_address":<base64, 67 bytes bdCommonAddr>,
"security_id":<base64, 8 bytes>, "security_key":<base64, 16 bytes>, "nat":...}}`.

Search doc (145/18): `{"members":[<online ids>], "lobby_slots":{"min":n,"max":n},
"ruleset_payload":{"filter":{...}}}`. A party searches once, from its leader, listing every
member; each member needs a seat. Players match only when their filters are identical.

Host doc (read by the game in this order): `update_time` u64, `update_id` u64, `game_id` u64,
`lobby_open` bool, `lobby_open_for_pres_join` bool, `player_state` object (online id ->
u32 state), `listen_server` `{host_address: base64 bdCommonAddr, host_player_id: u64}`,
`dedicated_server`, `team_balance` `{can_change_teams: bool}`, `ruleset_payload`,
`attachment`. Player states: 1 host, 2 connected peer, 3 an expected peer that timed out.

Backend doc: `update_id`, `update_time`, `lobby_id`, `create_time`, `players` (online id ->
`"expected"`), `dtls` `{security_id, security_key}` (the host's), `ruleset_payload`.

Pushes (inner `0x02`, `08 u32 event`, then `0A u64` ids, then `10 host doc, 10 backend doc`):

| event | ids | meaning |
| --- | --- | --- |
| 2005 create lobby | search id, lobby id, version | sent to the chosen host |
| 2006 update lobby | lobby id, version | asks the host to publish a new host doc (not an ack) |
| 2001 join lobby | search id, lobby id | sent to a guest once the host has synced |
| 2008 merge into lobby | search id, current lobby id, destination lobby id | |

The host echoes the `update_id` of the doc it composed; a sync is accepted only when that
equals the server's current version, and the version changes only when membership changes.
A sync doc must carry `lobby_open`, `lobby_open_for_pres_join`, `player_state`,
`listen_server`, `team_balance`, `ruleset_payload` and `attachment`. A host that closes its
lobby (`lobby_open` false) takes no more players.

### Pit Stop (bdMarketplace, service 80)

Context string `octane_switch`. Currencies 10 (Wumpa Coins), 11, 12.

| task | request | reply |
| --- | --- | --- |
| 245 getBalancesV3 | struct `{1: context, 2: maxNumResults}` | struct `{1: repeated {1: currency id, 2: zigzag amount, 3: 0}}` |
| 246 switchReconciliation | struct carrying an eShop receipt | empty struct |
| 165 getInventoryPaginated | `10 context, 08 u32 page (1-based), 08 u32 page size` | rows, one per owned item (below) |
| 154 purchaseSkus | `10 context, 08 u32 n, n x 08 u32 sku, 08 u32 n, n x 08 u32 quantity (1)` | none on success, then push 76 |

Inventory row: `0A u64 owner, 10 "nintendo", 08 u32 catalog item id, 08 u32 quantity (1),
08 u32 0, 13 blob (empty), 08 u32 0, 09 i64 expiry (0 = never), 06 u16 0, 08 u32 0`. The item
id is the catalog *item* a SKU grants, not the SKU.

A SKU grants a list of items at a full price; a bundle SKU's price is the sum of its parts'
prices that the player does not own yet. Owned SKUs cost 0. Items are non-consumable.

Push 76 (balance update): struct `{1: {1: {1: owner id, 2: "nintendo"}, 2: currency 10,
3: 0xFFFFFFFF, 4: 0, 5: zigzag balance}}`.

### Achievements engine (service 125)

All replies are struct replies.

| task | reply |
| --- | --- |
| 1 reportEvents | empty struct; then pushes (76 balance when coins changed, 75 achievement states) |
| 2 reportUserEvents | empty struct |
| 3 getAchievementStates | `{1: repeated state, 2: ""}`; field 2 must be present |
| 9 getUserState | `{1: "{}"}` (fires the refresh confirmation) |
| other | empty struct |

reportEvents struct: field 2 repeated event `{1: name, 2: timestamp, 3: repeated {1: key,
2: value}, 4: id}`.

getAchievementStates request fields: 2 kinds, 3 limit, 4 page token, 5 statuses, 6 names,
7 ids.

Achievement state: `1 name, 2 kind, 3 progress, 4 target, 5 status (2 active, 3 completed),
6 completion time, 7 period start, 8 period end, 9 completed flag, 11 id`. Push 75 is
`{1: repeated state}`.

- `pitstop_daily_refresh`: id 255, kind 1, target 2, daily period starting 15:00 UTC. Each
  `pitstop_daily_refresh` event with value 1 inside the current period is one refresh, two a
  day.
- `visit_pit_stop` ("Window Shopping"): id 11, kind 3, target 1, reward 25 coins; an 84-day
  cycle starting at unix 1585234800 in which it is active on days 0, 1, 4-8, 11-15, 18-22,
  25-29, 32-36, 39-41 (only the first 42 days carry it).
- Race rewards: event `end_race` with values `Track` (a hash of the track name), `Rank`,
  `Time`, `CompletedLap`, `RaceMode`, `Online`, `RewardGoldenWumpa`. Coins by track and
  position (1st, 2nd, 3rd, 4th and below) come from the game's map table
  (`data/race-rewards.tsv`). Mode 15: amount x min(laps, 8) / 3. Online races: x5 while the
  player's online racing time that UTC day is under 1800 s. Weekend (Friday 17:00 to Monday
  08:00 UTC): x2. Golden Wumpa: +200 after multipliers.

### Not answered by anything above

Leaderboards (service 4) are not implemented for CTR; an empty success is returned.

## 9. Identity decisions (OpenPak, not facts about the game)

- The Demonware user id and CTR's online id are the player's **network service account id**
  (nx-baas `baas_user_id`, hex, as u64): stable per OpenPak account, and the same number the
  Switch friend service hands the game for that player, so friend lookups, friend sessions
  and party members all line up without a translation table.
