package wire

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	w := &Writer{}
	w.Bool(true).U8(7).U32(0xDEADBEEF).U64(1<<40 + 5).String("octane_switch").Blob([]byte{1, 2, 3}).Struct([]byte{0x08, 0x01})
	r := NewReader(w.B)
	if v, err := r.Bool(); err != nil || !v {
		t.Fatal("bool", v, err)
	}
	if v, err := r.U8(); err != nil || v != 7 {
		t.Fatal("u8", v, err)
	}
	if v, err := r.U32(); err != nil || v != 0xDEADBEEF {
		t.Fatal("u32", v, err)
	}
	if v, err := r.U64(); err != nil || v != 1<<40+5 {
		t.Fatal("u64", v, err)
	}
	if v, err := r.String(); err != nil || v != "octane_switch" {
		t.Fatal("string", v, err)
	}
	if v, err := r.Blob(); err != nil || !bytes.Equal(v, []byte{1, 2, 3}) {
		t.Fatal("blob", v, err)
	}
	if v, err := r.Struct(); err != nil || !bytes.Equal(v, []byte{0x08, 0x01}) {
		t.Fatal("struct", v, err)
	}
	if r.More() {
		t.Fatal("bytes left")
	}
}

// The exact bytes of a few values, as docs/protocol.md §4 gives them.
func TestEncoding(t *testing.T) {
	w := &Writer{}
	w.U8(3).String("ab").Blob([]byte{0xFF})
	want, _ := hex.DecodeString("0303" + "10616200" + "13" + "0801000000" + "ff")
	if !bytes.Equal(w.B, want) {
		t.Fatalf("got %x\nwant %x", w.B, want)
	}
}

func TestWrongTag(t *testing.T) {
	r := NewReader([]byte{TU32, 1, 0, 0, 0})
	if _, err := r.U64(); err == nil {
		t.Fatal("u64 read a u32")
	}
	if r.Off != 0 {
		t.Fatal("a failed read moved the cursor")
	}
	if _, err := NewReader([]byte{TString, 'a'}).String(); err == nil {
		t.Fatal("unterminated string accepted")
	}
	if _, err := NewReader([]byte{TBlob, TU32, 9, 0, 0, 0, 1}).Blob(); err == nil {
		t.Fatal("blob longer than the buffer accepted")
	}
}

func TestU64List(t *testing.T) {
	w := &Writer{}
	w.U64(1).U64(2)
	// array of u64: 0x6E, typed byte count, raw count, raw values
	w.B = append(w.B, 100+TU64)
	w.U32(4 + 16)
	w.B = binary.LittleEndian.AppendUint32(w.B, 2)
	w.B = binary.LittleEndian.AppendUint64(w.B, 3)
	w.B = binary.LittleEndian.AppendUint64(w.B, 4)
	w.String("stop")
	r := NewReader(w.B)
	got := r.U64List(100)
	if len(got) != 4 || got[0] != 1 || got[3] != 4 {
		t.Fatalf("got %v", got)
	}
	if s, err := r.String(); err != nil || s != "stop" {
		t.Fatal("list overran", s, err)
	}
	if n := len(NewReader(w.B).U64List(3)); n != 3 {
		t.Fatalf("limit ignored: %d", n)
	}
}

func TestRestDropsTerminator(t *testing.T) {
	r := NewReader([]byte{TU8, 1, TU8, 2, 0})
	_, _ = r.U8()
	if got := r.Rest(); !bytes.Equal(got, []byte{TU8, 2}) {
		t.Fatalf("got %x", got)
	}
}

func TestRowsLayout(t *testing.T) {
	empty := Empty(9)
	r := NewReader(empty)
	_, _ = r.U64()
	if code, _ := r.U32(); code != OK {
		t.Fatal("error code", code)
	}
	if task, _ := r.U8(); task != 9 {
		t.Fatal("task", task)
	}
	if n, _ := r.U32(); n != 0 || r.More() {
		t.Fatal("an empty reply carries a total")
	}

	rows := Rows(6, func(w *Writer) uint32 { w.U32(42); return 1 })
	r = NewReader(rows)
	_, _ = r.U64()
	_, _ = r.U32()
	_, _ = r.U8()
	n, _ := r.U32()
	total, _ := r.U32()
	v, _ := r.U32()
	if n != 1 || total != 1 || v != 42 {
		t.Fatal("rows", n, total, v)
	}

	if f := Fail(Failed); len(f) != 9+5 {
		t.Fatalf("a failure carries more than transaction and error: %x", f)
	}
}

func TestStructReplyHasNoCounts(t *testing.T) {
	b := StructReply(245, []byte{0x0A, 0x00})
	// 0A u64 | 08 u32 | 03 task | 17 08 u32 len | bytes
	if b[14] != TU8 || b[15] != 245 || b[16] != TStruct || b[17] != TU32 || binary.LittleEndian.Uint32(b[18:]) != 2 {
		t.Fatalf("layout %x", b)
	}
}

func TestPB(t *testing.T) {
	if Zigzag(10000) != 20000 || Zigzag(-1) != 1 || Zigzag(1) != 2 {
		t.Fatal("zigzag")
	}
	p := PB{}.Varint(1, 150).Str(2, "hi").Signed(3, -2)
	want, _ := hex.DecodeString("089601" + "12026869" + "1803")
	if !bytes.Equal(p, want) {
		t.Fatalf("got %x want %x", []byte(p), want)
	}
	fs, err := ParsePB(p)
	if err != nil || len(fs) != 3 || fs[0].Value != 150 || string(fs[1].Data) != "hi" || fs[2].Value != 3 {
		t.Fatal(fs, err)
	}
	if _, err := ParsePB([]byte{0x12, 0x05, 'a'}); err == nil {
		t.Fatal("truncated field accepted")
	}
	if _, err := ParsePB([]byte{0x0D, 0, 0, 0, 0}); err == nil {
		t.Fatal("fixed32 accepted")
	}
}
