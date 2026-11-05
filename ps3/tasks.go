package ps3

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/binary"
	"log/slog"
	"net"
	"sync/atomic"
	"time"

	"github.com/openpak/demonware/wire"
)

//go:embed mw3_playlists.txt
var mw3Playlists []byte

// defaultFiles are the publisher files Modern Warfare 3 TU 1.24 asks for by name:
// playlists_tu24.aggr = [u16 BE 1][u16 BE 6][u16 BE size] + zlib of the playlist text
// (inflated into a 0x3C000 buffer, the file at most 0xF000 bytes), and social_TU24.cfg, which
// only has to exist or the game stops with error 256. The playlist text is from
// github.com/elliot1377/mw2-ps3-server (MIT licence).
func defaultFiles() map[string][]byte {
	text := append(bytes.ReplaceAll(mw3Playlists, []byte("\r\n"), []byte("\n")), 0)
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestCompression)
	_, _ = zw.Write(text)
	_ = zw.Close()
	aggr := binary.BigEndian.AppendUint16(nil, 1)
	aggr = binary.BigEndian.AppendUint16(aggr, 6)
	aggr = binary.BigEndian.AppendUint16(aggr, uint16(z.Len()))
	aggr = append(aggr, z.Bytes()...)
	return map[string][]byte{
		"playlists_tu24.aggr": aggr,
		"social_TU24.cfg":     []byte("// OpenPak\n"),
	}
}

type arg struct {
	tag byte
	str string
	buf []byte
	num uint64
}

// parseArgs reads a typed byte buffer into args (best effort, like the reference server).
func parseArgs(b []byte) []arg {
	sizes := map[byte]int{1: 1, 2: 1, 3: 1, 4: 2, 5: 2, 6: 2, 7: 4, 8: 4, 9: 8, 10: 8, 13: 4, 14: 8}
	var out []arg
	for p := 0; p < len(b) && len(out) < 64; {
		t := b[p]
		p++
		switch {
		case sizes[t] > 0:
			n := sizes[t]
			if p+n > len(b) {
				return out
			}
			var v uint64
			for i := n - 1; i >= 0; i-- {
				v = v<<8 | uint64(b[p+i])
			}
			out = append(out, arg{tag: t, num: v})
			p += n
		case t == wire.TString:
			e := bytes.IndexByte(b[p:], 0)
			if e < 0 {
				e = len(b) - p
			}
			out = append(out, arg{tag: t, str: string(b[p : p+e])})
			p += e + 1
		case t == wire.TBlob:
			if p+5 > len(b) {
				return out
			}
			n := int(binary.LittleEndian.Uint32(b[p+1:]))
			if n < 0 || p+5+n > len(b) {
				return out
			}
			out = append(out, arg{tag: t, buf: b[p+5 : p+5+n]})
			p += 5 + n
		default:
			return out
		}
	}
	return out
}

func strs(a []arg) (s []string) {
	for _, x := range a {
		if x.tag == wire.TString {
			s = append(s, x.str)
		}
	}
	return
}
func blobs(a []arg) (s [][]byte) {
	for _, x := range a {
		if x.tag == wire.TBlob {
			s = append(s, x.buf)
		}
	}
	return
}
func u64s(a []arg) (s []uint64) {
	for _, x := range a {
		if x.tag == wire.TU64 {
			s = append(s, x.num)
		}
	}
	return
}

var txn atomic.Uint64

// reply is the Modern Warfare 3 task reply: [u64 transaction][u32 error][u8 task][u32 count][u32 total][results].
// The client matches replies first-in-first-out, so every task must be answered, in order.
func reply(task byte, errCode uint32, results ...[]byte) []byte {
	w := &wire.Writer{}
	w.U64(txn.Add(1)).U32(errCode).U8(task)
	if errCode != 0 {
		return w.B
	}
	w.U32(uint32(len(results))).U32(uint32(len(results)))
	for _, r := range results {
		w.Raw(r)
	}
	return w.B
}

func safeName(n string) string {
	b := make([]byte, 0, len(n))
	for i := 0; i < len(n) && len(b) < 96; i++ {
		c := n[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '.' || c == '_' || c == '-' {
			b = append(b, c)
		}
	}
	if len(b) == 0 {
		return "_"
	}
	return string(b)
}

// task answers one remote task of a logged-in player. plain after the service byte is
// [typed u8 task][typed args]. Every request is logged by shape only (counts and sizes).
func (s *Server) task(sess *Session, svc byte, msg []byte, log *slog.Logger) []byte {
	if len(msg) < 2 || msg[0] != wire.TU8 {
		log.Info("lobby task", "service", svc, "shape", "unreadable")
		return reply(0, 0)
	}
	task := msg[1]
	args := parseArgs(msg[2:])
	log.Info("lobby task", "service", svc, "task", task, "args", wire.Describe(msg[2:]))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st := s.cfg.Store
	names, bl := strs(args), blobs(args)
	switch {
	case svc == svcStorage && task == 7 && len(names) > 0: // publisher file by name
		if d, ok := s.files[names[0]]; ok {
			return reply(task, 0, (&wire.Writer{}).Blob(d).B)
		}
		log.Info("publisher file not found", "name_len", len(names[0]))
		return reply(task, bdNoFile)
	case svc == svcStorage && task == 3 && len(names) > 0: // user file by name
		d, ok, err := st.File(ctx, sess.Title, sess.PID, safeName(names[0]))
		if err != nil {
			log.Error("user file read failed", "err", err)
			return reply(task, 1)
		}
		if !ok { // "no such file" makes MW3 start fresh stats; a made-up blank file would be rejected
			return reply(task, bdNoFile)
		}
		return reply(task, 0, (&wire.Writer{}).Blob(d).B)
	case svc == svcStorage && len(bl) > 0 && len(names) > 0: // upload or update a user file
		if len(bl[0]) > 1<<20 {
			return reply(task, 1)
		}
		if err := st.PutFile(ctx, sess.Title, sess.PID, safeName(names[0]), bl[0]); err != nil {
			log.Error("user file write failed", "err", err)
			return reply(task, 1)
		}
		var id [8]byte
		_, _ = rand.Read(id[:])
		return reply(task, 0, (&wire.Writer{}).U64(binary.LittleEndian.Uint64(id[:])>>1).B)
	case svc == svcStats && task == 3: // stats by entity: one empty row per player asked for
		var rows [][]byte
		for _, e := range u64s(args) {
			w := &wire.Writer{}
			w.U64(e).I64(1).U64(0).String(sess.OnlineID).U32(0)
			for i := 0; i < 32; i++ {
				w.I32(0)
			}
			rows = append(rows, w.B)
		}
		return reply(task, 0, rows...)
	case svc == svcUtility && task == 6: // server time
		return reply(task, 0, (&wire.Writer{}).U32(uint32(time.Now().Unix())).B)
	case svc == svcMatchmaker && task == 1: // a host opens a game
		id := make([]byte, 8)
		_, _ = rand.Read(id)
		info := msg[2:]
		if len(info) > 16<<10 {
			info = info[:16<<10]
		}
		if err := st.PutGame(ctx, Game{ID: id, Title: sess.Title, PID: sess.PID, Info: info}); err != nil {
			log.Error("game store failed", "err", err)
			return reply(task, 1)
		}
		if s.cfg.Rewards != nil {
			s.cfg.Rewards.Hosted(sess.Title, id, sess.OnlineID)
		}
		log.Info("game opened")
		return reply(task, 0, (&wire.Writer{}).Blob(id).B)
	case svc == svcMatchmaker && task == 3: // a host closes a game
		for _, id := range bl {
			if n, _ := st.DeleteGames(ctx, sess.PID, id); n > 0 {
				if s.cfg.Rewards != nil {
					s.cfg.Rewards.Ended(id, sess.OnlineID)
				}
				log.Info("game closed")
			}
		}
		return reply(task, 0)
	}
	// Find (service 21, task 5) and everything not decoded yet: an empty success. MW3 hosts a game
	// itself after a few empty searches. The shape is logged above so a capture can decode it.
	return reply(task, 0)
}

// bandwidth answers MW3's bandwidth test (service 18, raw, little endian, answered as message 5):
//
//	start    [00][type]  -> [u8 0][7 x u32 packetSize, numPackets, senderInitialWait, sendDuration,
//	                          receiverInitialWait, receiveDuration, lingerDuration][u16 port][IPv4][8-byte cookie]
//	finalize [01][5 x u32] -> [u8 0][5 x u32 result]
//
// numPackets stays below 20 (the first byte of an upload packet is its sequence number; 20 and 30
// would look like NAT and IP probes). The finalize result claims 250000 bytes in 1000 ms, enough
// for an 18-player host (>= 580000 bit/s).
func (s *Server) bandwidth(data []byte) []byte {
	head := binary.LittleEndian.AppendUint64(nil, txn.Add(1))
	if len(data) > 0 && data[0] == 1 {
		out := append(head, 0)
		for _, v := range []uint32{250000, 1000, 0, 0, 0} {
			out = binary.LittleEndian.AppendUint32(out, v)
		}
		return out
	}
	out := append(head, 0)
	for _, v := range []uint32{512, 10, 0, 1000, 0, 1000, 0} {
		out = binary.LittleEndian.AppendUint32(out, v)
	}
	out = binary.LittleEndian.AppendUint16(out, 3074)
	ip := net.IPv4(127, 0, 0, 1)
	if s.cfg.PublicIP != nil {
		ip = s.cfg.PublicIP
	}
	out = append(out, ip.To4()...)
	return append(out, "BWTEST01"...)
}
