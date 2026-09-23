# Changelog — demonware

`git log` stays the source of truth; this file is the readable summary.

## Unreleased

First version. Not tagged, not deployed.

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
