package wire

import (
	"encoding/binary"
	"errors"
)

// PB builds protobuf bytes for struct-buffer tasks. Only varint and length-delimited fields
// occur in what the games exchange.
type PB []byte

func (p PB) Varint(field int, v uint64) PB {
	p = binary.AppendUvarint(p, uint64(field)<<3)
	return binary.AppendUvarint(p, v)
}

// Signed writes a zigzag-encoded int64, which is how the games encode signed amounts.
func (p PB) Signed(field int, v int64) PB { return p.Varint(field, Zigzag(v)) }

func (p PB) Bytes(field int, b []byte) PB {
	p = binary.AppendUvarint(p, uint64(field)<<3|2)
	p = binary.AppendUvarint(p, uint64(len(b)))
	return append(p, b...)
}

func (p PB) Str(field int, s string) PB { return p.Bytes(field, []byte(s)) }

func Zigzag(v int64) uint64 { return uint64(v<<1) ^ uint64(v>>63) }

// Field is one decoded protobuf field.
type Field struct {
	Num   int
	Value uint64 // varint fields
	Data  []byte // length-delimited fields; nil for varints
}

var errPB = errors.New("wire: malformed protobuf")

// ParsePB decodes one message level. Only wire types 0 and 2 are accepted.
func ParsePB(b []byte) ([]Field, error) {
	var out []Field
	for i := 0; i < len(b); {
		key, n := binary.Uvarint(b[i:])
		if n <= 0 || key>>3 == 0 {
			return nil, errPB
		}
		i += n
		f := Field{Num: int(key >> 3)}
		switch key & 7 {
		case 0:
			v, n := binary.Uvarint(b[i:])
			if n <= 0 {
				return nil, errPB
			}
			f.Value, i = v, i+n
		case 2:
			l, n := binary.Uvarint(b[i:])
			if n <= 0 || l > uint64(len(b)-i-n) {
				return nil, errPB
			}
			i += n
			f.Data = b[i : i+int(l)]
			if f.Data == nil {
				f.Data = []byte{}
			}
			i += int(l)
		default:
			return nil, errPB
		}
		if out = append(out, f); len(out) > 4096 {
			return nil, errPB
		}
	}
	return out, nil
}
