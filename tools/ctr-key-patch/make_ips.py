#!/usr/bin/env python3
"""Build the CTR key patch: an IPS32 that swaps the RSA-2048 modulus embedded in Crash Team
Racing Nitro-Fueled's main for OpenPak's, so the game accepts auth replies signed with
DEMONWARE_CTR_SIGNING_KEY. Only the modulus changes; the exponent, the verifier and every
instruction stay as shipped.

    make_ips.py <uncompressed main NSO> <openpak-ctr-public.pem> <out.ips>

The NSO is hactool's --uncompressed output (header included), so file offsets are IPS offsets,
which is what Atmosphère and the emulators' IPS loaders expect.
"""
import hashlib
import sys

from cryptography.hazmat.primitives.serialization import Encoding, PublicFormat, load_pem_public_key

BUILD_ID = bytes.fromhex("1C689518406930512C13DDF4217E7676") + bytes(16)  # v983040, the final update
MODULUS_OFFSET = 0x40B4E61  # NSO file offset; mapped 0x40B4D61, inside the SPKI at 0x40B4D40
ORIGINAL_SPKI_SHA1 = "f474dda2e170d9700e329935e35ef7b3e9df9c1c"  # Demonware's key, as in lobby/crypto.go
SPKI_PREFIX = bytes.fromhex("30820122300d06092a864886f70d01010105000382010f003082010a0282010100")


def build(nso: bytes, pem: bytes) -> bytes:
    assert nso[:4] == b"NSO0", "not an uncompressed NSO"
    assert nso[0x40:0x60] == BUILD_ID, f"wrong build {nso[0x40:0x60].hex()}"
    spki_at = MODULUS_OFFSET - len(SPKI_PREFIX)
    original = nso[spki_at : spki_at + 294]
    assert hashlib.sha1(original).hexdigest() == ORIGINAL_SPKI_SHA1, "embedded key is not Demonware's"
    ours = load_pem_public_key(pem).public_bytes(Encoding.DER, PublicFormat.SubjectPublicKeyInfo)
    assert len(ours) == 294 and ours.startswith(SPKI_PREFIX) and ours.endswith(bytes.fromhex("0203010001")), \
        "key must be RSA-2048 with exponent 65537"
    modulus = ours[len(SPKI_PREFIX) : len(SPKI_PREFIX) + 256]
    return b"IPS32" + MODULUS_OFFSET.to_bytes(4, "big") + len(modulus).to_bytes(2, "big") + modulus + b"EEOF"


def apply(nso: bytes, ips: bytes) -> bytes:
    """Minimal IPS32 applier, for the self-check."""
    assert ips[:5] == b"IPS32" and ips[-4:] == b"EEOF"
    out, i = bytearray(nso), 5
    while i < len(ips) - 4:
        off, n = int.from_bytes(ips[i : i + 4], "big"), int.from_bytes(ips[i + 4 : i + 6], "big")
        out[off : off + n] = ips[i + 6 : i + 6 + n]
        i += 6 + n
    return bytes(out)


if __name__ == "__main__":
    nso, pem = open(sys.argv[1], "rb").read(), open(sys.argv[2], "rb").read()
    ips = build(nso, pem)
    patched = apply(nso, ips)
    ours = load_pem_public_key(pem).public_bytes(Encoding.DER, PublicFormat.SubjectPublicKeyInfo)
    spki_at = MODULUS_OFFSET - len(SPKI_PREFIX)
    assert patched[spki_at : spki_at + 294] == ours
    assert sum(a != b for a, b in zip(nso, patched)) <= 256 and len(patched) == len(nso)
    open(sys.argv[3], "xb").write(ips)
    print(sys.argv[3], len(ips), "bytes, sha256", hashlib.sha256(ips).hexdigest())
