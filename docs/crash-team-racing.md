# Crash Team Racing Nitro-Fueled: from feasibility study to server

This carries forward `servers/crash-team-racing` (a documentation-only feasibility study,
2026-09-09 to 2026-09-15), which this repository supersedes. The study's verdict and the
answers to its unknowns are kept here; its experiment list is kept only where it still
applies.

## What the study established (still true)

- CTR:NF on Switch is a **Demonware** title, not NEX, NPLN or BAAS: it logs in to
  `lavender-switch-auth3.prod.demonware.net` and then `lavender-switch-lobby.prod.demonware.net`,
  seen live in OpenPak's own citron tree (`emulators/citron/src/core/hle/service/sockets/
  sfdnsres.cpp` and `bsd.cpp`, the exact-host redirect and its port-443 companion).
- Its symbols include `igNetTaskLsgGetTicket` and `igNetTaskLsgAuthenticateTicket`: auth3 then
  the Lobby Service Gateway, the BitDemon family's own names.
- Racing is peer to peer: there is no server simulation to rebuild. The server's job is
  identity, matchmaking, and the economy around the races.
- Nintendo supplies only identity (the NSA id_token), which is what auth3 receives.

## The study's unknowns, answered

| Unknown | Answer (source in `provenance.md`) |
| --- | --- |
| U1 what is inside the TLS | auth3 is HTTPS with a JSON body; the lobby is *not* TLS: a raw TCP stream (hello, then length-prefixed frames, then an AES/HMAC channel keyed by the ticket). `docs/protocol.md` §2-3. |
| U2 what auth3 wants | The NSA id_token inside `extra_data`; the reply is a 128-byte ticket readable in the clear (magic `DE AD BD EF`). No key baked into the client is needed for the lobby. CTR additionally checks an `X-Signature` on the reply, which passes once the client carries OpenPak's key (`tools/ctr-key-patch`). |
| U3 the matchmaking protocol | Async matchmaking, service 145, with JSON search/host/backend documents and pushes 2001/2005/2006/2008; friend sessions on service 138/14. `docs/protocol.md` §8. |
| U4 does the client hard-fail before the network | No, but it hard-fails on an unsigned auth reply (the `X-Signature` check). That is a client-side gate: a server cannot pass it alone. |
| U5 hardware parity | Still open. A console needs the Demonware names in DNS (nn-sssl-dns), the OpenPak CA trusted for the auth3 certificate, and the signature mod. |

## What remains of the study's plan

- **E1 (does real Demonware accept an OpenPak console?)** was never run and no longer gates
  anything: the server exists. It still matters for one decision: if the real service accepts
  OpenPak consoles, redirecting `lavender-*` to this server is optional for CTR.
- **E6 (hardware)** is the next real test, and it needs the CTR key patch (shipped by `openpak.nro` and built into the emulators).
- **Non-goals of the study** (leaderboard parity, Grand Prix live content, anti-cheat,
  telemetry) stand. The Pit Stop economy, listed there as a non-goal, is implemented because
  the game's own data made it cheap and the shop is unusable without balances.

## Open

- The lobby port: the citron hook rewrites port 443 for the lobby host as well as auth3, but
  the lobby is plain TCP and the fact source serves it on 3074. See `provenance.md`.
- The `X-Signature` key: solved by [`tools/ctr-key-patch`](../tools/ctr-key-patch), a
  data-only swap of the embedded modulus, found and verified in OpenPak's own dump of the final
  update. Not yet run.
