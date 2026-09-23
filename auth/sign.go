package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
)

// Signer produces CTR's X-Signature: RSA-PSS over SHA-256 with a zero-length salt, RSA-2048,
// exponent 65537 (docs/protocol.md §2). Go's rsa.SignPSS cannot be asked for an empty salt
// (0 means "as long as possible"), so the PSS encoding is done here and the private-key
// operation is blinded. Every signature is verified before it is used.
type Signer struct{ key *rsa.PrivateKey }

// LoadSigner reads a PEM RSA private key (PKCS#1 or PKCS#8).
func LoadSigner(path string) (*Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(raw)
	if blk == nil {
		return nil, errors.New("signing key: not PEM")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		key = k
	} else if k, err := x509.ParsePKCS8PrivateKey(blk.Bytes); err == nil {
		key, _ = k.(*rsa.PrivateKey)
	}
	if key == nil {
		return nil, errors.New("signing key: not an RSA private key")
	}
	return NewSigner(key)
}

// NewSigner checks the key's shape and that it signs.
func NewSigner(key *rsa.PrivateKey) (*Signer, error) {
	if key.N.BitLen() != 2048 || key.E != 65537 {
		return nil, errors.New("signing key: must be RSA-2048 with exponent 65537")
	}
	if err := key.Validate(); err != nil {
		return nil, fmt.Errorf("signing key: %w", err)
	}
	s := &Signer{key}
	if _, err := s.Sign([]byte("self-test")); err != nil {
		return nil, err
	}
	return s, nil
}

// PublicKeyDER is the signer's public key as the game embeds it (DER SubjectPublicKeyInfo).
func (s *Signer) PublicKeyDER() []byte {
	der, _ := x509.MarshalPKIXPublicKey(&s.key.PublicKey) // cannot fail for an RSA key
	return der
}

// Sign returns the base64 signature of body.
func (s *Signer) Sign(body []byte) (string, error) {
	digest := sha256.Sum256(body)
	em := pssEncode(digest[:], s.key.N.BitLen()-1)

	n, d, e := s.key.N, s.key.D, big.NewInt(int64(s.key.E))
	r, err := rand.Int(rand.Reader, n)
	if err != nil {
		return "", err
	}
	rInv := new(big.Int).ModInverse(r, n)
	if rInv == nil {
		return "", errors.New("signing: blinding failed")
	}
	m := new(big.Int).SetBytes(em)
	m.Mul(m, new(big.Int).Exp(r, e, n)).Mod(m, n) // blind
	sig := new(big.Int).Exp(m, d, n)
	sig.Mul(sig, rInv).Mod(sig, n) // unblind

	out := sig.FillBytes(make([]byte, (n.BitLen()+7)/8))
	if err := rsa.VerifyPSS(&s.key.PublicKey, crypto.SHA256, digest[:], out,
		&rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthAuto}); err != nil {
		return "", fmt.Errorf("signing: self-check: %w", err)
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

// pssEncode is EMSA-PSS-ENCODE (RFC 8017 §9.1.1) with SHA-256 and an empty salt.
func pssEncode(mHash []byte, emBits int) []byte {
	emLen := (emBits + 7) / 8
	h := sha256.New()
	h.Write(make([]byte, 8))
	h.Write(mHash)
	H := h.Sum(nil)

	db := make([]byte, emLen-len(H)-1)
	db[len(db)-1] = 0x01
	mask := mgf1(H, len(db))
	for i := range db {
		db[i] ^= mask[i]
	}
	db[0] &= 0xFF >> (8*emLen - emBits)
	return append(append(db, H...), 0xBC)
}

func mgf1(seed []byte, n int) []byte {
	var out []byte
	for c := uint32(0); len(out) < n; c++ {
		h := sha256.New()
		h.Write(seed)
		h.Write([]byte{byte(c >> 24), byte(c >> 16), byte(c >> 8), byte(c)})
		out = h.Sum(out)
	}
	return out[:n]
}
