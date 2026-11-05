# Demonware

OpenPak's Demonware Server.

| | |
| --- | --- |
| Networks | Nintendo Switch, PlayStation 3 (Modern Warfare 3, in development) |
| Protocol | Demonware: auth3 (HTTPS), encrypted lobby (TCP 3074), NAT probes (UDP 3074), remote tasks |
| Image | `ghcr.io/openpak/demonware` |
| Licence | AGPL-3.0-only |

## Titles

| Game | Network | Title ID | Module |
| --- | --- | --- | --- |
| [Diablo III: Eternal Collection](https://openpak.org/titles#t-108) | Nintendo Switch | `01001B300B9BE000` | `titles/diablo3` |
| [Crash Team Racing Nitro-Fueled](https://openpak.org/titles#t-20) | Nintendo Switch | `0100F9F00C696000` | `titles/ctr` |
| Call of Duty: Modern Warfare 3 (2011) | PlayStation 3 | BLES01428 / BLJM60422 | `ps3` (off unless `DEMONWARE_PS3_TICKET_KEY` is set; not yet seen by a real game) |

## Networks

| Network | Reached through |
| --- | --- |
| Nintendo Switch | The games' Demonware hostnames, answered by `nn-sssl-dns` (see [deploy](docs/deploy.md)) |
| PlayStation 3 | The game's Demonware names (exact names in the one signed network list, `ps3` platform); same TCP and UDP 3074, told apart by the first bytes ([docs/ps3.md](docs/ps3.md)) |

## Run

```sh
DEMONWARE_PUBLIC_IP=127.0.0.1 DEMONWARE_REQUIRE_ACCOUNT=0 CERT_FILE=… KEY_FILE=… go run ./cmd/demonwared
```

| Variable | Meaning |
| --- | --- |
| `DEMONWARE_TITLES` | games auth3 admits (default `diablo-3,crash-team-racing`) |
| `DEMONWARE_PUBLIC_IP` | required: IPv4 address consoles reach this server on |
| `NX_INTERNAL_KEY` | required: nx-baas internal key |
| `CERT_FILE`, `KEY_FILE` | certificate for the auth3 names |
| `DEMONWARE_REQUIRE_ACCOUNT` | `1` by default; `0` admits unproven players, local tests only |
| `DEMONWARE_DATA` | sessions, CTR wallets, Diablo III settings |

The full variable list, ports and production steps are in [docs/deploy.md](docs/deploy.md).

## Docs

- [protocol.md](docs/protocol.md): hosts, auth3, lobby, NAT and remote task formats
- [ps3.md](docs/ps3.md): Modern Warfare 3 on PlayStation 3: protocol, tasks, unknowns, Pier and console test steps
- [deploy.md](docs/deploy.md): environment, ports, DNS, Traefik, quadlet, firewall, run and test
- [crash-team-racing.md](docs/crash-team-racing.md): Crash Team Racing feasibility study

## Licence

AGPL-3.0-only. See [LICENSE](LICENSE).

## Not supported

- Crash Team Racing lobby merges (push 2008), and async-matchmaking and Pit Stop tasks beyond those in docs/protocol.md section 8 (openpak/prds#117).
