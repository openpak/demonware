# OpenPak CTR key patch

Crash Team Racing Nitro-Fueled (`0100F9F00C696000`) verifies every auth3 reply's
`X-Signature` against the RSA-2048 key embedded in its `main`. It is Demonware's own key, the
same one Diablo III carries; Diablo III just never checks a signature. This patch replaces the
key's 256-byte modulus with `openpak-ctr-public.pem`, so the game accepts replies signed with
`DEMONWARE_CTR_SIGNING_KEY`. It is a key swap, not a signature bypass: the verifier, exponent
and every instruction stay as shipped.

It is the one game patch OpenPak ships. Everything else is fixed on the console (the OpenPak
CA in the system store); CTR checks the signature in its own code against its own key copy,
so there is no system service to fix it in.

## Target

- Final update v983040, main NSO build ID
  `1C689518406930512C13DDF4217E767600000000000000000000000000000000`. CTR gets no more
  updates, so this is the only build.
- IPS32, one record: NSO offset `0x40B4E61` (mapped `0x40B4D61` plus the 0x100 header, as
  Atmosphère's loader and the emulators' IPS loaders expect), 256 bytes. 271 bytes in all,
  SHA-256 `5f44ed4e75889b035f4f16d49715211dfcffcdc7876b1a91e28bfda947f72c54`.

## Where it ships

- Console: `openpak.nro` installs it to
  `sd:/atmosphere/exefs_patches/openpak_ctr_key/` with the other OpenPak patch sets.
  Removing that folder and rebooting undoes it.
- Emulators: Ryujinx, Citron and Eden carry the same bytes built in and apply them to this
  build when OpenPak is on.

## Evidence

- The key is the only DER SubjectPublicKeyInfo in `main` (NSO offset `0x40B4E40`, 294 bytes,
  SHA-1 `f474dda2e170d9700e329935e35ef7b3e9df9c1c`, the key `lobby/crypto.go` names).
- Two code sites load it: the auth-reply handler at mapped `0xd568`, a few instructions after
  it reads the `X-Signature` header string (`0xd4e0`), imports it (length `0x126`) and verifies
  with libtomcrypt PKCS#1 PSS; and the lobby key wrap at `0x14584`, taken only when a
  connection flag is set. The server therefore also accepts a lobby key wrapped with OpenPak's
  public key (`lobby.Server.PatchedKeys`).
- `make_ips.py` refuses a wrong build, a key that is not Demonware's, or a key that is not
  RSA-2048/65537, applies its output to the NSO and checks only the modulus changed. A
  signature made with the private key (PSS, SHA-256, zero salt, the server's scheme) verifies
  against the key read back out of the patched `main`.
- Not yet run on hardware or in an emulator.

## Rebuild

```sh
python3 make_ips.py <uncompressed main NSO> openpak-ctr-public.pem <build-id>.ips
```

The NSO comes from the v983040 update NSP (`nstool` for the ExeFS, then
`hactool -t nso0 --uncompressed`).

## The private key

Owner-local at `~/openpak-ctr-private/openpak-ctr-signing.pem` (mode 0600).
Deploy it to the server as `DEMONWARE_CTR_SIGNING_KEY`. Back it up privately: losing it means
a new public key, a new patch and reinstalling it everywhere. Never put it in a repository,
an image or an NRO.
