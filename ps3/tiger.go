package ps3

import (
	_ "embed"
	"encoding/binary"
)

// Tiger-192 (Anderson and Biham, original 0x01 padding) with the standard S-boxes.
// tiger_sboxes.bin (4 x 256 big-endian uint64) and the Python it was checked against are
// from github.com/elliot1377/mw2-ps3-server (MIT licence). Demonware uses Tiger for the IV
// of its 3DES channel (bdCryptoUtils::calculateInitialVector).

//go:embed tiger_sboxes.bin
var tigerBoxes []byte

var tigerT [4][256]uint64

func init() {
	if len(tigerBoxes) != 8192 {
		panic("ps3: tiger_sboxes.bin has the wrong size")
	}
	for i := 0; i < 4; i++ {
		for j := 0; j < 256; j++ {
			tigerT[i][j] = binary.BigEndian.Uint64(tigerBoxes[i*2048+j*8:])
		}
	}
}

func tigerRound(a, b, c, x, mul uint64) (uint64, uint64, uint64) {
	c ^= x
	a -= tigerT[0][c&0xff] ^ tigerT[1][(c>>16)&0xff] ^ tigerT[2][(c>>32)&0xff] ^ tigerT[3][(c>>48)&0xff]
	b += tigerT[3][(c>>8)&0xff] ^ tigerT[2][(c>>24)&0xff] ^ tigerT[1][(c>>40)&0xff] ^ tigerT[0][(c>>56)&0xff]
	b *= mul
	return a, b, c
}

func tigerPass(a, b, c uint64, x *[8]uint64, mul uint64) (uint64, uint64, uint64) {
	for i := 0; i < 8; i++ {
		switch i % 3 {
		case 0:
			a, b, c = tigerRound(a, b, c, x[i], mul)
		case 1:
			b, c, a = tigerRound(b, c, a, x[i], mul)
		default:
			c, a, b = tigerRound(c, a, b, x[i], mul)
		}
	}
	return a, b, c
}

func tigerSchedule(x *[8]uint64) {
	x[0] -= x[7] ^ 0xA5A5A5A5A5A5A5A5
	x[1] ^= x[0]
	x[2] += x[1]
	x[3] -= x[2] ^ (^x[1] << 19)
	x[4] ^= x[3]
	x[5] += x[4]
	x[6] -= x[5] ^ (^x[4] >> 23)
	x[7] ^= x[6]
	x[0] += x[7]
	x[1] -= x[0] ^ (^x[7] << 19)
	x[2] ^= x[1]
	x[3] += x[2]
	x[4] -= x[3] ^ (^x[2] >> 23)
	x[5] ^= x[4]
	x[6] += x[5]
	x[7] -= x[6] ^ 0x0123456789ABCDEF
}

// Tiger returns the 24-byte Tiger-192 digest of data.
func Tiger(data []byte) [24]byte {
	a, b, c := uint64(0x0123456789ABCDEF), uint64(0xFEDCBA9876543210), uint64(0xF096A5B4C3B2E187)
	msg := append(append([]byte(nil), data...), 0x01)
	for (len(msg)+8)%64 != 0 {
		msg = append(msg, 0)
	}
	msg = binary.LittleEndian.AppendUint64(msg, uint64(len(data))*8)
	for off := 0; off < len(msg); off += 64 {
		var x [8]uint64
		for i := range x {
			x[i] = binary.LittleEndian.Uint64(msg[off+i*8:])
		}
		aa, bb, cc := a, b, c
		a, b, c = tigerPass(a, b, c, &x, 5)
		tigerSchedule(&x)
		c, a, b = tigerPass(c, a, b, &x, 7)
		tigerSchedule(&x)
		b, c, a = tigerPass(b, c, a, &x, 9)
		a ^= aa
		b -= bb
		c += cc
	}
	var out [24]byte
	binary.LittleEndian.PutUint64(out[0:], a)
	binary.LittleEndian.PutUint64(out[8:], b)
	binary.LittleEndian.PutUint64(out[16:], c)
	return out
}

// IV is bdCryptoUtils::calculateInitialVector: the first 8 bytes of Tiger of the seed (u32 LE).
func IV(seed uint32) []byte {
	d := Tiger(binary.LittleEndian.AppendUint32(nil, seed))
	return d[:8]
}
