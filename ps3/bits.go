package ps3

// bdBitBuffer: bits are read and written least significant bit first; the first bit says
// whether every value carries a 5-bit type tag. The PS3 auth request and reply and the lobby
// login use it, also for Modern Warfare 3 (docs/ps3.md).

const (
	bdUint32 = 8
)

type bitWriter struct {
	bits  []byte // one bit per element
	typed bool
}

func newBitWriter() *bitWriter {
	w := &bitWriter{typed: true}
	w.bits = append(w.bits, 1)
	return w
}

func (w *bitWriter) raw(v uint64, n int) {
	for i := 0; i < n; i++ {
		w.bits = append(w.bits, byte(v>>i)&1)
	}
}

func (w *bitWriter) u32(v uint32) {
	if w.typed {
		w.raw(bdUint32, 5)
	}
	w.raw(uint64(v), 32)
}

func (w *bitWriter) bytes(b []byte) {
	for _, c := range b {
		w.raw(uint64(c), 8)
	}
}

func (w *bitWriter) out() []byte {
	o := make([]byte, (len(w.bits)+7)/8)
	for i, b := range w.bits {
		if b != 0 {
			o[i>>3] |= 1 << (i & 7)
		}
	}
	return o
}

type bitReader struct {
	b     []byte
	pos   int
	typed bool
}

func newBitReader(b []byte) *bitReader {
	r := &bitReader{b: b}
	r.typed = r.raw(1) == 1
	return r
}

func (r *bitReader) raw(n int) uint64 {
	var v uint64
	for i := 0; i < n; i++ {
		var bit uint64
		if r.pos>>3 < len(r.b) {
			bit = uint64(r.b[r.pos>>3]>>(r.pos&7)) & 1
		}
		v |= bit << i
		r.pos++
	}
	return v
}

func (r *bitReader) u32() uint32 {
	if r.typed {
		r.raw(5)
	}
	return uint32(r.raw(32))
}

func (r *bitReader) left() int { return len(r.b)*8 - r.pos }

// findMagic scans every bit offset from 1 for a little-endian 32-bit magic followed by 124
// more bytes and returns the 128 bytes at the first match (the lobby login carries the server
// ticket at an arbitrary bit offset).
func findMagic(payload []byte, magic uint32) []byte {
	r := &bitReader{b: payload}
	for start := 1; start+128*8 <= len(payload)*8; start++ {
		r.pos = start
		if uint32(r.raw(32)) == magic {
			r.pos = start
			t := make([]byte, 128)
			for i := range t {
				t[i] = byte(r.raw(8))
			}
			return t
		}
	}
	return nil
}
