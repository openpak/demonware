package ps3

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"time"
)

// The NP ticket np-account issues (platforms/ps3/infra/np-common/ticket): a firmware-4.93
// envelope [40 00 00 00][0x3000 element holding the fields], then 0x3010, two zero blocks and
// the OpenPak extension 0x3012 = "OPKT1" + JSON claims + an Ed25519 signature over the
// envelope and the claims. The signature is what proves the player; Demonware only needs the
// public key. Everything about the issuing side is in that package; this file mirrors its
// Verify, including the request cookie np-common v0.3.0 appends (docs/ps3.md).

// ErrTicket is any ticket that does not verify.
var ErrTicket = errors.New("invalid or expired NP ticket")

// Identity are the signed claims (same JSON as np-common/ticket.Identity).
type Identity struct {
	AccountID string `json:"account"`
	LinkID    string `json:"link"`
	OnlineID  string `json:"online"`
	Country   string `json:"country"`
	Service   string `json:"service"`
	ConsoleID string `json:"console"`
	Issued    int64  `json:"iat"`
	Expires   int64  `json:"exp"`
}

// NPTicket is a verified ticket plus the game's request cookie, if the ticket carries one.
type NPTicket struct {
	Identity
	// Cookie is the 24 bytes the game asked for in its ticket request. Demonware encrypts the
	// auth reply with it (3DES), so a ticket without one cannot be served.
	Cookie []byte
}

var opkt = []byte("OPKT1")

// LoadPublicKey reads an Ed25519 key from a PEM file: a public key (PKIX) or the private key
// (PKCS8) the ticket signer uses, from which the public half is taken.
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("ticket key must be PEM")
	}
	if k, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if pk, ok := k.(ed25519.PublicKey); ok {
			return pk, nil
		}
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("ticket key must be an Ed25519 public or PKCS8 private key")
	}
	ed, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("ticket key must be Ed25519")
	}
	return ed.Public().(ed25519.PublicKey), nil
}

type tlv struct {
	tag uint16
	val []byte
}

// elems walks the 4-byte-header elements; tags 0x3000 and 0x3010 have a 6-byte header.
func elems(b []byte) ([]tlv, int) {
	var out []tlv
	p := 0
	for p+4 <= len(b) {
		tag := binary.BigEndian.Uint16(b[p:])
		hdr, n := 4, int(binary.BigEndian.Uint16(b[p+2:]))
		if tag == 0x3000 || tag == 0x3010 {
			if p+6 > len(b) {
				break
			}
			hdr, n = 6, int(binary.BigEndian.Uint32(b[p+2:]))
		}
		if n < 0 || p+hdr+n > len(b) {
			break
		}
		out = append(out, tlv{tag, b[p+hdr : p+hdr+n]})
		p += hdr + n
	}
	return out, p
}

// ParseTicket finds the OpenPak ticket anywhere in the auth request body and verifies it.
func ParseTicket(blob []byte, pub ed25519.PublicKey, now time.Time) (*NPTicket, error) {
	i := bytes.Index(blob, opkt)
	if i < 8 || len(pub) != ed25519.PublicKeySize {
		return nil, ErrTicket
	}
	// The 0x3012 element header (4 bytes) sits right before the magic; the envelope ends there.
	// It starts at the version word 40 00 00 00: look back for it.
	end := i - 4
	start := bytes.LastIndex(blob[:end], []byte{0x40, 0, 0, 0})
	for start >= 0 {
		top, used := elems(blob[start+4 : end])
		if len(top) >= 1 && top[0].tag == 0x3000 && start+4+used == end {
			break
		}
		start = bytes.LastIndex(blob[:start], []byte{0x40, 0, 0, 0})
	}
	if start < 0 || binary.BigEndian.Uint16(blob[end:]) != 0x3012 {
		return nil, ErrTicket
	}
	n := int(binary.BigEndian.Uint16(blob[end+2:]))
	last := end + 4 + n
	if last > len(blob) || n < len(opkt)+ed25519.SignatureSize {
		return nil, ErrTicket
	}
	payload := blob[end+4 : last-ed25519.SignatureSize]
	sig := blob[last-ed25519.SignatureSize : last]
	signed := append(append([]byte("openpak-np-ticket-v1\x00"), blob[start:end]...), payload...)
	if !ed25519.Verify(pub, signed, sig) {
		return nil, ErrTicket
	}
	var t NPTicket
	if json.Unmarshal(payload[len(opkt):], &t.Identity) != nil {
		return nil, ErrTicket
	}
	ms := now.UnixMilli()
	if t.Expires <= ms || t.Issued > ms+30000 || t.Expires <= t.Issued || t.AccountID == "" || t.OnlineID == "" {
		return nil, ErrTicket
	}
	top, _ := elems(blob[start+4 : end])
	fields, _ := elems(top[0].val)
	for idx, f := range fields {
		// fields 0..11 are the firmware's twelve; np-common v0.3.0 appends the cookie as the 13th.
		if idx >= 12 && len(f.val) == 24 {
			t.Cookie = append([]byte(nil), f.val...)
			break
		}
	}
	return &t, nil
}

// PID is the stable 64-bit player number: np-common onlineid.AccountID, the NP account id the
// ticket and the native sign-in carry. Erasure derives it from the account id alone.
func PID(accountID string) uint64 {
	sum := sha256.Sum256([]byte("openpak-np-native-account-id-v1\x00" + accountID))
	return binary.BigEndian.Uint64(sum[:8])
}
