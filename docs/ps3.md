# Demonware on PlayStation 3: Call of Duty: Modern Warfare 3

Status: v0.8.0, **built and unit-tested, not yet seen by a real MW3**. Nothing here has run against the game; the
parts marked **[I]** are inferred from the community protocol notes and settled by the first capture (M0 below).
Plan and owner decisions: `prds/repos/ps3-games-plan.md` (openpak/prds#132).

Source of the protocol: github.com/elliot1377/mw2-ps3-server (MIT), `docs/PROTOCOL.md`, reverse engineered from the MW2
and MW3 PS3 EBOOTs. The Tiger S-boxes, the playlist text and the message shapes come from there; the code is ours.
The Python server it came with is not used.

## What is here

| Piece | Where |
| --- | --- |
| Tiger-192 and the 3DES channel (IV = Tiger of a seed) | `ps3/tiger.go`, `ps3/server.go` |
| NP ticket check (np-account's Ed25519-signed ticket) | `ps3/ticket.go` |
| Auth (message 18 to 19) and lobby (login 7, connection id 4, tasks, raw replies 5) | `ps3/server.go` |
| Tasks: publisher files, user files (`mpdata`), stats, server time, matchmaking create/delete, bandwidth test | `ps3/tasks.go` |
| Postgres tables `dw_ps3_sessions`, `dw_ps3_files`, `dw_ps3_games` | `ps3/store.go` |
| One TCP 3074 shared with the Switch lobby | `ps3/sniff.go` |
| Rewards session reports | `ps3/rewards.go` |
| Account standing check (account core, gRPC) | `ps3/core.go` |
| Account erasure | `internal/erase` (`Eraser.PS3`) |

UDP 3074 needs nothing new: the IP and NAT discovery packets of the PS3 (`1e ...` and `14 ...`, replies `1f 02 00` and `15 02 00`)
have the same shape as the Switch ones, so the existing `nat` package answers both.

## The two connections (TCP 3074)

Both start on the same port and are told apart by the first eight bytes (see `ps3.Split`, and plan section "Port 3074"):

| Connection | First bytes | Sniff |
| --- | --- | --- |
| Switch lobby | `c8 00 00 00 c8 00 00 00 ...` (u32 200, u32 200) | byte 4 is `c8`, bytes 5 to 7 are zero: the existing lobby |
| MW3 lobby | `c8 00 00 00 ff ff 00 00` | PS3 |
| MW3 auth | frame `u32 length`, `00` (not encrypted), `12` (message 18) | PS3 |

**Auth.** Message 18 carries a bit buffer `{typed u32 title}` and the NP ticket (byte aligned) **[I]**. The server verifies the ticket's
Ed25519 signature, checks the account in the account core, and answers message 19: `{u32 700, u32 seed}`, then the 152-byte
bdAuthTicket encrypted with 3DES-CBC using the **ticket's 24-byte cookie** as the key and `Tiger(seed)[:8]` as the IV, then our
128-byte lobby ticket. The lobby ticket is `EXDC` + 124 random bytes; only its SHA-256 is stored (Postgres), and the 3DES session key
is `HMAC-SHA256(DEMONWARE_PS3_SECRET, ticket)[:24]`, so nothing secret is at rest.

**Lobby.** The client sends the 8-byte header, then message 7 (a bit stream holding our lobby ticket at any bit offset). The server answers
the connection id (message 4) and from then on every client frame is `01 | u32 seed | 3DES-CBC(session key)` carrying
`[u32 hash][u8 service][typed u8 task][typed args]`. Every task is answered, in order.

## Tasks answered (MW3 TU 1.24)

| Service | Task | Answer |
| --- | --- | --- |
| 10 storage | 7 | publisher file by name: `playlists_tu24.aggr` (zlib of the playlist text), `social_TU24.cfg`; any other name is error 1000 |
| 10 storage | 3 | user file by name from Postgres; none is error 1000 (MW3 starts fresh stats) |
| 10 storage | other with a blob and a name | stores the user file (`mpdata`, 12288 bytes with a CRC-32); 1 MiB cap |
| 4 stats | 3 | one empty row per entity asked for |
| 12 utility | 6 | server time |
| 21 matchmaking | 1, 3 | create returns an 8-byte id and stores the game; delete removes it. **Find (5) returns an empty success**, so MW3 hosts its own game |
| 18 bandwidth | any | raw reply as message 5; start: 10 packets of 512 bytes to our UDP 3074 with cookie `BWTEST01`; finalize claims 250000 bytes in 1000 ms |
| anything else | | an empty success, and the argument shape is logged (`wire.Describe`: numbers, string and blob lengths) |

## What is not known yet (settled by the captures)

1. **The hostnames MW3 resolves.** MW2's are `mw2-ps3-auth.mmp3.demonware.net`, `mw2-ps3-lsg.live.mmp3.demonware.net`,
   `mw2-stun.{us,eu}.demonware.net` and the `*.ps3.iw4.iwnet.infinityward.com` set (mw2-ps3-server `dnsserver.py`). MW3's are **[I]** the same pattern
   with their own prefix and are not in the sources I could read. They come from the MW3 `default_mp.self` strings and the first capture; they go into the one
   signed network list on the `ps3` platform as **exact names**.
2. **Whether np-account's ticket can serve MW3.** The game puts a 24-byte cookie in its ticket request and Demonware uses it as the 3DES key. np-account's ticket has no
   cookie today (`np-common/ticket`), so auth is refused with `ticket carries no 24-byte cookie`. np-account has to (a) take the cookie from the game's ticket
   request, (b) add it as a 24-byte field after the service id, and (c) let `Verify` accept a longer body (the envelope is now measured, not a fixed 272 bytes).
   This is a separate change in `np-account`/`np-common` (card to open). The first Pier run captures what the firmware sends (`NP_CAPTURE_DIR`).
3. **Find results.** The search arguments are logged by shape; the result layout (`bdMatchMakingInfo` for service 21) is not known, so finding another
   host's game is not served yet. Private matches use the NP party/invite path and peer to peer, which the lobby does not need.
4. **Joins.** Joining is peer to peer, so the lobby cannot see who joins a game. Rewards sessions therefore see one player per room and emit nothing
   (the rewards session rule needs two). The hook is `EmitRewards.Join`; it gets wired when the capture or an NP signal proves a join.
5. **NAT type.** MW3 asks the server to answer from another IP and port; with one public IP it will show a moderate NAT, not open.
6. **The post-2018 account bug** (a 2011 binary bug, `setsid/mw3-ps3-psn-fix`) is triggered by what Demonware sent for new accounts. With our own ids it
   may never fire; the Pier and PS3 runs below test it. If it does fire, the only fix is a game patch, which needs the owner's exception (plan, decision 3).
7. **Rewards metric.** `game_sessions.call-of-duty-modern-warfare-3` and the `np` identity namespace must exist in the rewards core; until they do, events
   would be dropped (none are emitted yet anyway).

## Configuration (all unset = PS3 off, the Switch behaviour is unchanged)

| Variable | Meaning |
| --- | --- |
| `DEMONWARE_PS3_TICKET_KEY` | PEM of the Ed25519 key np-account signs tickets with (public or PKCS8 private; only the public half is used). Setting it turns PS3 on |
| `DEMONWARE_PS3_SECRET_FILE` | file with 32 or more random bytes keying the lobby session keys (required with the above) |
| `ACCOUNT_GRPC_ADDR`, `ACCOUNT_INTERNAL_KEY` | account core, used for the account check (the same variables as erasure). `DEMONWARE_REQUIRE_ACCOUNT=0` skips the check (local only) |
| `REWARDS_EVENTS_URL`, `REWARDS_EVENTS_KEY` | rewards-emit; unset is a no-op |

Production wiring, ports and the quadlet: [deploy.md](deploy.md). No new port: PS3 uses TCP and UDP 3074.

## Logging and privacy

Every connection logs: `auth ok` / `auth refused` (reason and size only), `lobby login`, one `lobby task` line per task (service, task, argument
shape) and `lobby closed`. Players appear as a per-process tag (`redact.Tag`); never a ticket, cookie, key, account id, online id or file content.
`TestMW3AuthLobbyStorageAndGames` asserts this.

## Erasure

`DELETE /internal/accounts/{account_id}/data` also deletes the account's `dw_ps3_sessions`, `dw_ps3_files` and `dw_ps3_games` rows. The PS3 player number is
derived from the account id (`ps3.PID`, the same function as np-common's `onlineid.AccountID`), so no link lookup is needed.

## Testing: Pier first, then the real PS3

Agents run Pier themselves (rules in the plan, "Pier testing"): load below 8, no Harbor, Wharf or other Pier instance running (`pgrep -a -f 'harbor|wharf|pier|rpcs3'`),
one emulator test at a time, `scratch/pier/run-pier.sh`, profile in `~/.config/pier`. **Pier needs MW3 installed**; it is not on the HDD yet (Terraria, Skate 3 and Minecraft are).

### M0: discovery (no MW3 server needed)
1. Owner supplies MW3 (see the plan, "Dumps needed"). Agent installs it into Pier, and the decrypted `default_mp.self` goes to `scratch/ps3/games/<TITLEID>/`.
2. Agent runs `strings` on the ELF for `demonware`, `iwnet` and `activision` names; the list is the candidate exact names.
3. In Pier: sign in as the test account, start MW3, Multiplayer, Play Online. With `NP_CAPTURE_DIR` on np-account, record the ticket request the game makes (cookie field).
4. Read the resolver log for the console's address during that run for the names it asked.

### M1 to M3: with the server enabled (after np-account carries the cookie)
1. Add the names to the signed list (`ps3` platform, exact names) and deploy `demonware` with the three variables above.
2. **Play Online.** Expect: `auth ok`, `lobby login`, tasks for `playlists_tu24.aggr` and `social_TU24.cfg`, no error 256, the multiplayer menu opens.
3. **Private Match.** Create a match; expect `game opened` and then `game closed` when leaving; the server needs no join information.
4. **Quick Match.** The search returns empty and the game hosts its own; expect the `lobby task` line of service 21 task 5 with its argument shape (that shape is the capture we need).
5. Second player: Pier plus the owner's real PS3, or a second Pier instance (second test account and its own profile directory) where the game allows it.

### The same steps on the owner's PS3
Waits on the game-process redirect in ps3-patcher (openpak/prds#125). The owner: patcher installed and switched to OpenPak, restart, sign in with an OpenPak account, start MW3,
**Multiplayer > Play Online**, then **Private Match**, then **Quick Match**; after each, tell the agent the on-screen result (menu reached, any `Communication with the Activision
servers has been interrupted`, error numbers). The server logs say the rest.

## Not supported

Modern Warfare 2 (its `b4` header is refused), clans, leaderboards beyond empty rows, Elite, the Demonware ban and file services other than those listed above.
