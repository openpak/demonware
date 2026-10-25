# Demonware

OpenPak's Demonware Server.

| | |
| --- | --- |
| Networks | Nintendo Switch |
| Protocol | Demonware: auth3 (HTTPS), encrypted lobby (TCP 3074), NAT probes (UDP 3074), remote tasks |
| Image | `ghcr.io/openpak/demonware` |
| Licence | AGPL-3.0-only |

## Titles

| Game | Network | Title ID | Module |
| --- | --- | --- | --- |
| [Diablo III: Eternal Collection](https://openpak.org/titles#t-108) | Nintendo Switch | `01001B300B9BE000` | `titles/diablo3` |
| [Crash Team Racing Nitro-Fueled](https://openpak.org/titles#t-20) | Nintendo Switch | `0100F9F00C696000` | `titles/ctr` |

## Networks

| Network | Reached through |
| --- | --- |
| Nintendo Switch | The games' Demonware hostnames, answered by `nn-sssl-dns` (see [deploy](docs/deploy.md)) |

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
- [evidence.md](docs/evidence.md): test results
- [deploy.md](docs/deploy.md): environment, ports, DNS, Traefik, quadlet, firewall, run and test
- [notes.md](docs/notes.md): design rationale, provenance, what is not done, client notice
- [provenance.md](docs/provenance.md): where each protocol fact came from
- [crash-team-racing.md](docs/crash-team-racing.md): Crash Team Racing feasibility study

## Licence

AGPL-3.0-only. See [LICENSE](LICENSE).
