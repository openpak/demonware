package lobby

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
)

// Message types of the lobby transport (docs/protocol.md §3).
const (
	frameMark   = 0xAB
	msgHello    = 0x81 // server: version, connection id
	msgAuth     = 0x82 // client: ticket + tag
	msgAuthOK   = 0x83 // server: check
	msgAuthFail = 0x84 // server: u32 error
	msgSealed   = 0x85 // either way: encrypted

	innerTask  = 0x86 // client: task request
	innerReply = 0x01 // server: task reply
	innerPush  = 0x02 // server: push message
)

const (
	versionLow  = 210
	versionHigh = 220
)

// channel is the key set derived at the end of the handshake.
type channel struct {
	tag, check    [8]byte
	inMAC, outMAC []byte // HMAC-SHA1 keys, client->server and server->client
	inAES, outAES cipher.Block
	outSeq        uint32
}

// expand is the handshake's SHA-1 key expansion: block i is HMAC(key, previous block | label | i),
// with no previous block for i = 1.
func expand(key, label []byte, n int) []byte {
	var out, block []byte
	for i := byte(1); len(out) < n; i++ {
		m := hmac.New(sha1.New, key)
		m.Write(block)
		m.Write(label)
		m.Write([]byte{i})
		block = m.Sum(nil)
		out = append(out, block...)
	}
	return out[:n]
}

// derive turns the handshake transcript and the ticket's lobby key into the channel keys.
func derive(transcript, lobbyKey []byte) (*channel, error) {
	digest := sha1.Sum(transcript)
	m := hmac.New(sha1.New, digest[:])
	m.Write(lobbyKey)
	secret := m.Sum(nil)

	chal := expand(secret, []byte("CLIENTCHAL"), 16)
	data := expand(secret, []byte("BDDATA"), 72)
	ch := &channel{inMAC: data[0:20], outMAC: data[20:40]}
	copy(ch.tag[:], chal[:8])
	copy(ch.check[:], chal[8:])
	var err error
	if ch.inAES, err = aes.NewCipher(data[40:56]); err != nil {
		return nil, err
	}
	if ch.outAES, err = aes.NewCipher(data[56:72]); err != nil {
		return nil, err
	}
	return ch, nil
}

// wrapped is the lobby key after the optional transformation with the game's embedded public key.
func wrapped(lobbyKey []byte) []byte { return expand(lobbyKey, gamePublicKey, 24) }

func mac8(key, data []byte) []byte {
	m := hmac.New(sha1.New, key)
	m.Write(data)
	return m.Sum(nil)[:8]
}

// seal frames an inner message as a 0x85.
func (ch *channel) seal(inner byte, payload []byte) ([]byte, error) {
	plain := binary.LittleEndian.AppendUint32(nil, uint32(len(payload)))
	plain = append(append(plain, inner), payload...)
	if pad := len(plain) % aes.BlockSize; pad != 0 {
		plain = append(plain, make([]byte, aes.BlockSize-pad)...)
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	ch.outSeq++
	bodyLen := 1 + 4 + len(iv) + len(plain) + 8
	f := binary.LittleEndian.AppendUint32(make([]byte, 0, 5+bodyLen), uint32(1+bodyLen))
	f = append(f, frameMark, msgSealed)
	f = binary.LittleEndian.AppendUint32(f, ch.outSeq)
	f = append(f, iv...)
	ct := make([]byte, len(plain))
	cipher.NewCBCEncrypter(ch.outAES, iv).CryptBlocks(ct, plain)
	f = append(f, ct...)
	return append(f, mac8(ch.outMAC, f)...), nil
}

var errSealed = errors.New("lobby: bad sealed frame")

// open checks and decrypts a whole 0x85 frame (length prefix included).
func (ch *channel) open(frame []byte) (inner byte, payload []byte, err error) {
	const head = 4 + 1 + 1 + 4 + aes.BlockSize
	if len(frame) < head+aes.BlockSize+8 || (len(frame)-head-8)%aes.BlockSize != 0 {
		return 0, nil, errSealed
	}
	body, tag := frame[:len(frame)-8], frame[len(frame)-8:]
	if !hmac.Equal(mac8(ch.inMAC, body), tag) {
		return 0, nil, errors.New("lobby: frame MAC mismatch")
	}
	iv := body[10:head]
	plain := make([]byte, len(body)-head)
	cipher.NewCBCDecrypter(ch.inAES, iv).CryptBlocks(plain, body[head:])
	n := binary.LittleEndian.Uint32(plain)
	if uint64(n) > uint64(len(plain)-5) {
		return 0, nil, errSealed
	}
	return plain[4], plain[5 : 5+n], nil
}

// gamePublicKey is the RSA-2048 public key (DER SubjectPublicKeyInfo) embedded in the game,
// used only as the label of the optional lobby-key transformation. It is a constant of the
// client, not a secret (docs/provenance.md).
var gamePublicKey = mustHex("" +
	"30820122300d06092a864886f70d01010105000382010f003082010a0282010100c0a20b1f6cb81b1270ed1aef306c75" +
	"9dc1088999f02ac8ac2fc7d5d03b612939f38f6239daf12011e792e916242296099eac19cd243e58c6408678d7df7077" +
	"cbde8042b138f31d6a3c98e485dbfb533a8647ce58b1d3d70b833d146bda40241f162b0e4922e4b763ffaa40c244dfdc" +
	"3f8c1e60b46f3edab24e50cafc624b62c7e1775e83cde0b5fcc6aaa0c26b28cc8aa7957b1e67e05bafc65449e6ac7a8d" +
	"1de67d120494c3234a006058336fe79419fff6e0c64050b79d0ecddfe7925d8494130661bc4475547054774cc0287dfc" +
	"c99a9238d4d5eef3274466132c06f064e7ecf875fd15d41b91459d4a3f40e935537ffc9661e1487421f00420413002d2" +
	"f90203010001")

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
