// Package wire is the Demonware typed byte buffer, the task reply envelope, and the small
// protobuf subset the struct-buffer tasks use. docs/protocol.md §4 is the format.
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync/atomic"
)

// Type tags of the typed byte buffer.
const (
	TBool   = 0x01
	TU8     = 0x03
	TU16    = 0x06
	TI32    = 0x07
	TU32    = 0x08
	TI64    = 0x09
	TU64    = 0x0A
	TString = 0x10
	TBlob   = 0x13
	TStruct = 0x17
	tArray  = 100 // array of t is tagged 100+t
)

// ErrShort is a buffer that ends before the value it announces.
var ErrShort = errors.New("wire: buffer too short")

// Writer appends typed values.
type Writer struct{ B []byte }

func (w *Writer) Bool(v bool) *Writer {
	b := byte(0)
	if v {
		b = 1
	}
	w.B = append(w.B, TBool, b)
	return w
}

func (w *Writer) U8(v byte) *Writer { w.B = append(w.B, TU8, v); return w }

func (w *Writer) U16(v uint16) *Writer {
	w.B = binary.LittleEndian.AppendUint16(append(w.B, TU16), v)
	return w
}

func (w *Writer) U32(v uint32) *Writer {
	w.B = binary.LittleEndian.AppendUint32(append(w.B, TU32), v)
	return w
}

func (w *Writer) U64(v uint64) *Writer {
	w.B = binary.LittleEndian.AppendUint64(append(w.B, TU64), v)
	return w
}

func (w *Writer) I64(v int64) *Writer {
	w.B = binary.LittleEndian.AppendUint64(append(w.B, TI64), uint64(v))
	return w
}

func (w *Writer) String(s string) *Writer {
	w.B = append(append(append(w.B, TString), s...), 0)
	return w
}

func (w *Writer) sized(tag byte, p []byte) *Writer {
	w.B = append(w.B, tag)
	w.U32(uint32(len(p)))
	w.B = append(w.B, p...)
	return w
}

// Blob writes 13 | 08 u32 len | bytes.
func (w *Writer) Blob(p []byte) *Writer { return w.sized(TBlob, p) }

// Struct writes a struct buffer (protobuf bytes), framed like a blob.
func (w *Writer) Struct(p []byte) *Writer { return w.sized(TStruct, p) }

// Raw appends bytes that are already typed.
func (w *Writer) Raw(p []byte) *Writer { w.B = append(w.B, p...); return w }

// Reader walks typed values.
type Reader struct {
	B   []byte
	Off int
}

func NewReader(b []byte) *Reader { return &Reader{B: b} }

// More reports whether an unread byte remains other than the request terminator.
func (r *Reader) More() bool { return r.Off < len(r.B) && !(r.Off == len(r.B)-1 && r.B[r.Off] == 0) }

// Next is the tag of the next value, or 0 at the end.
func (r *Reader) Next() byte {
	if r.Off >= len(r.B) {
		return 0
	}
	return r.B[r.Off]
}

func (r *Reader) take(tag byte, n int) ([]byte, error) {
	if r.Off >= len(r.B) {
		return nil, ErrShort
	}
	if got := r.B[r.Off]; got != tag {
		return nil, fmt.Errorf("wire: tag %#02x at %d, want %#02x", got, r.Off, tag)
	}
	if r.Off+1+n > len(r.B) {
		return nil, ErrShort
	}
	v := r.B[r.Off+1 : r.Off+1+n]
	r.Off += 1 + n
	return v, nil
}

func (r *Reader) Bool() (bool, error) {
	v, err := r.take(TBool, 1)
	if err != nil {
		return false, err
	}
	return v[0] != 0, nil
}

func (r *Reader) U8() (byte, error) {
	v, err := r.take(TU8, 1)
	if err != nil {
		return 0, err
	}
	return v[0], nil
}

func (r *Reader) U32() (uint32, error) {
	v, err := r.take(TU32, 4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(v), nil
}

func (r *Reader) U64() (uint64, error) {
	v, err := r.take(TU64, 8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(v), nil
}

func (r *Reader) String() (string, error) {
	if r.Off >= len(r.B) || r.B[r.Off] != TString {
		if r.Off >= len(r.B) {
			return "", ErrShort
		}
		return "", fmt.Errorf("wire: tag %#02x at %d, want string", r.B[r.Off], r.Off)
	}
	for end := r.Off + 1; end < len(r.B); end++ {
		if r.B[end] == 0 {
			s := string(r.B[r.Off+1 : end])
			r.Off = end + 1
			return s, nil
		}
	}
	return "", ErrShort
}

func (r *Reader) sized(tag byte) ([]byte, error) {
	if r.Off >= len(r.B) {
		return nil, ErrShort
	}
	if r.B[r.Off] != tag {
		return nil, fmt.Errorf("wire: tag %#02x at %d, want %#02x", r.B[r.Off], r.Off, tag)
	}
	save := r.Off
	r.Off++
	n, err := r.U32()
	if err != nil || r.Off+int(n) > len(r.B) {
		r.Off = save
		return nil, ErrShort
	}
	v := append([]byte(nil), r.B[r.Off:r.Off+int(n)]...)
	r.Off += int(n)
	return v, nil
}

func (r *Reader) Blob() ([]byte, error)   { return r.sized(TBlob) }
func (r *Reader) Struct() ([]byte, error) { return r.sized(TStruct) }

// U64List reads any run of typed u64s and u64 arrays, stopping at anything else.
func (r *Reader) U64List(limit int) []uint64 {
	var out []uint64
	for len(out) < limit {
		switch r.Next() {
		case TU64:
			v, err := r.U64()
			if err != nil {
				return out
			}
			out = append(out, v)
		case tArray + TU64:
			save := r.Off
			r.Off++
			if _, err := r.U32(); err != nil || r.Off+4 > len(r.B) { // byte count
				r.Off = save
				return out
			}
			n := int(binary.LittleEndian.Uint32(r.B[r.Off:]))
			r.Off += 4
			for i := 0; i < n && r.Off+8 <= len(r.B) && len(out) < limit; i++ {
				out = append(out, binary.LittleEndian.Uint64(r.B[r.Off:]))
				r.Off += 8
			}
		default:
			return out
		}
	}
	return out
}

// Rest is what is left, without the request's trailing NUL.
func (r *Reader) Rest() []byte {
	v := r.B[min(r.Off, len(r.B)):]
	if n := len(v); n > 0 && v[n-1] == 0 {
		v = v[:n-1]
	}
	r.Off = len(r.B)
	return append([]byte(nil), v...)
}

// ---- task replies ----

var txn atomic.Uint64

// Error codes a task reply can carry.
const (
	OK      = 0
	Pending = 200
	Failed  = 1 // any other value fails the task without a timeout
)

// Rows builds a task reply with results. fill writes the results and returns how many.
func Rows(task byte, fill func(w *Writer) uint32) []byte {
	w := &Writer{}
	w.U64(txn.Add(1)).U32(OK).U8(task)
	var body Writer
	n := uint32(0)
	if fill != nil {
		n = fill(&body)
	}
	w.U32(n)
	if n > 0 {
		w.U32(n).Raw(body.B)
	}
	return w.B
}

// Empty is a successful reply with no results.
func Empty(task byte) []byte { return Rows(task, nil) }

// Fail is a reply that fails the task.
func Fail(code uint32) []byte {
	w := &Writer{}
	return w.U64(txn.Add(1)).U32(code).B
}

// StructReply is the reply of a struct-buffer task: the struct follows the task byte directly.
func StructReply(task byte, sb []byte) []byte {
	w := &Writer{}
	return w.U64(txn.Add(1)).U32(OK).U8(task).Struct(sb).B
}

// Push is the payload of a pushed message: the event type, then fill's values.
func Push(event uint32, fill func(w *Writer)) []byte {
	w := &Writer{}
	w.U32(event)
	if fill != nil {
		fill(w)
	}
	return w.B
}
