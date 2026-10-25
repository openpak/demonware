# Notes: demonware

## Overview

OpenPak's Demonware server: auth3, the encrypted lobby and the remote tasks that Activision's
Demonware titles call on Nintendo Switch. One module, one image (`ghcr.io/openpak/demonware`),
one process for every title.

## Harbor client notice

> **Current Switch client (2026-10-13): Harbor**, at
> `~/REPOS/Openpak/emulators/harbor`. Harbor is an independent
> OpenPak project based on Eden. Citron, Ryujinx (desktop and Android), and
> the former OpenPak Eden checkouts have been removed. References to those
> forks below record historical evidence only: do not run their commands,
> reinstall them, or select them for current work. Use Harbor's README and
> current handoff for builds and testing; old results do not establish Harbor parity.

## Provenance and supersession

Built clean-room from the facts in [`protocol.md`](protocol.md); where every fact came
from is in [`provenance.md`](provenance.md). This repository supersedes the
`platforms/switch/games/crash-team-racing` feasibility study, whose content and open questions are carried in
[`crash-team-racing.md`](crash-team-racing.md).

## Why one process for both titles

The games dial the lobby on TCP 3074 and their NAT probes on UDP 3074, fixed in the client, and
both resolve to the same OpenPak address. Only one process can own those ports, and a lobby
connection only says which game it is after its ticket is read. So the title comes from the
ticket (auth3 stamps it in), and `DEMONWARE_TITLES` only chooses which games auth3 admits.

## Not done

Not done, for either title: server-reported presence (the console reports its own through
nx-baas; there is no game-server presence intake), leaderboards, hero storage, mail, Challenge
Rifts, season rotation, search filters for Diablo III.

## Disclaimer

This project is not affiliated with or endorsed by Nintendo, Activision, Blizzard, Beenox or
Demonware. It contains no game code, assets, keys or captures. Licence: AGPL-3.0-only.
