package lobby

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/openpak/demonware/account"
	"github.com/openpak/demonware/session"
	"github.com/openpak/demonware/wire"
)

func unhex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// expand is HKDF-Expand with SHA-1; RFC 5869 test case 4 checks it independently.
func TestExpandRFC5869(t *testing.T) {
	prk := unhex("9b6c18c432a7bf8f0e71c8eb88f4b30baa2ba243")
	info := unhex("f0f1f2f3f4f5f6f7f8f9")
	want := unhex("085a01ea1b10f36933068b56efa5ad81a4f14b822f5b091568a9cdd4f155fda2c22e422478d305f3f896")
	if got := expand(prk, info, 42); !bytes.Equal(got, want) {
		t.Fatalf("got %x", got)
	}
}

func TestGamePublicKey(t *testing.T) {
	if len(gamePublicKey) != 294 {
		t.Fatalf("DER is %d bytes, want 294", len(gamePublicKey))
	}
	sum := sha1.Sum(gamePublicKey)
	if hex.EncodeToString(sum[:]) != gamePublicKeySHA1 {
		t.Fatalf("DER changed: sha1 %x", sum)
	}
}

// Vectors computed from fixed inputs and cross-checked with an independent Python
// implementation of the same derivation (hmac/hashlib); a change to the derivation shows up
// here before it shows up on a console.
const (
	gamePublicKeySHA1 = "f474dda2e170d9700e329935e35ef7b3e9df9c1c"
	vecTag            = "d29f162bb9e62da7"
	vecCheck          = "aa79dc18bc5a2548"
	vecOutMAC         = "3d584aadee4ab924675b1df71613f0a17a6d05f9"
	vecWrapped        = "a2ae8141e4c68b7332a2835d8cfd425003a6cdb8c07ecb3b"
)

func TestDeriveVector(t *testing.T) {
	transcript := []byte("openpak demonware transcript")
	key := bytes.Repeat([]byte{0x24}, 24)
	ch, err := derive(transcript, key)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string][2]string{
		"tag":     {hex.EncodeToString(ch.tag[:]), vecTag},
		"check":   {hex.EncodeToString(ch.check[:]), vecCheck},
		"outMAC":  {hex.EncodeToString(ch.outMAC), vecOutMAC},
		"wrapped": {hex.EncodeToString(wrapped(key)), vecWrapped},
	} {
		if got[0] != got[1] {
			t.Errorf("%s = %s, want %s", name, got[0], got[1])
		}
	}
}

func TestSealOpen(t *testing.T) {
	server, _ := derive([]byte("t"), bytes.Repeat([]byte{1}, 24))
	client := &channel{inMAC: server.outMAC, outMAC: server.inMAC, inAES: server.outAES, outAES: server.inAES}
	for _, n := range []int{0, 1, 11, 12, 100} {
		payload := bytes.Repeat([]byte{0x5A}, n)
		f, err := server.seal(innerReply, payload)
		if err != nil {
			t.Fatal(err)
		}
		if binary.LittleEndian.Uint32(f) != uint32(len(f)-4) || f[4] != frameMark || f[5] != msgSealed {
			t.Fatalf("frame header % x", f[:6])
		}
		inner, got, err := client.open(f)
		if err != nil || inner != innerReply || !bytes.Equal(got, payload) {
			t.Fatalf("n=%d: %v %x", n, err, got)
		}
		f[len(f)-9] ^= 1
		if _, _, err := client.open(f); err == nil {
			t.Fatal("a tampered frame opened")
		}
	}
	if server.outSeq != 5 {
		t.Fatalf("sequence %d", server.outSeq)
	}
}

// bits packs values LSB-first, the way a bit buffer does, so the ticket lands unaligned.
type bits struct {
	b []byte
	n int
}

func (w *bits) put(v uint64, count int) {
	for i := 0; i < count; i++ {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		w.b[len(w.b)-1] |= byte(v>>uint(i)&1) << uint(w.n%8)
		w.n++
	}
}

func TestFindTicketUnaligned(t *testing.T) {
	ticket := make([]byte, session.Size)
	binary.LittleEndian.PutUint32(ticket, session.Magic)
	for i := 4; i < len(ticket); i++ {
		ticket[i] = byte(i * 7)
	}
	w := &bits{}
	w.put(5, 3)
	w.put(200, 32)
	for _, b := range ticket {
		w.put(uint64(b), 8)
	}
	w.put(0, 5)
	if got := locateTicket(w.b); !bytes.Equal(got, ticket) {
		t.Fatalf("got %x", got)
	}
	if locateTicket(bytes.Repeat([]byte{0x11}, 200)) != nil {
		t.Fatal("found a ticket in noise")
	}
}

// client drives a handshake against a real listener.
type client struct {
	t     *testing.T
	c     net.Conn
	nonce [8]byte
	ch    *channel
}

func (cl *client) readFrame() []byte {
	cl.t.Helper()
	_ = cl.c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hdr [4]byte
	if _, err := io.ReadFull(cl.c, hdr[:]); err != nil {
		cl.t.Fatalf("read: %v", err)
	}
	f := make([]byte, 4+binary.LittleEndian.Uint32(hdr[:]))
	copy(f, hdr[:])
	if _, err := io.ReadFull(cl.c, f[4:]); err != nil {
		cl.t.Fatalf("read: %v", err)
	}
	return f
}

// handshake runs hello, 0x81, 0x82 with ticket signed by key, and returns the server's reply frame.
func (cl *client) handshake(ticket, key []byte) []byte {
	le := binary.LittleEndian
	copy(cl.nonce[:], "noncenon")
	hello := le.AppendUint32(nil, 200)
	hello = le.AppendUint32(hello, 200)
	hello = le.AppendUint32(hello, 210)
	hello = le.AppendUint32(hello, 220)
	hello = le.AppendUint32(hello, 0x1FFFFF)
	hello = append(hello, cl.nonce[:]...)
	if _, err := cl.c.Write(hello); err != nil {
		cl.t.Fatal(err)
	}
	f81 := cl.readFrame()
	if f81[5] != msgHello || le.Uint32(f81[6:]) != 220 {
		cl.t.Fatalf("0x81 % x", f81)
	}
	payload81 := f81[6:]

	w := &bits{}
	w.put(1, 32)
	w.put(2, 3)
	for _, b := range ticket {
		w.put(uint64(b), 8)
	}
	body := append([]byte{msgAuth}, w.b...)
	f82 := le.AppendUint32(nil, uint32(1+len(body)+8))
	f82 = append(append(f82, frameMark), body...)

	var tr bytes.Buffer
	tr.Write(hello[8:])
	tr.Write(le.AppendUint32(nil, uint32(len(payload81)+2)))
	tr.Write([]byte{frameMark, msgHello})
	tr.Write(payload81)
	tr.Write(f82)
	server, _ := derive(tr.Bytes(), key)
	f82 = append(f82, server.tag[:]...)
	if _, err := cl.c.Write(f82); err != nil {
		cl.t.Fatal(err)
	}
	reply := cl.readFrame()
	if reply[5] == msgAuthOK && bytes.Equal(reply[6:14], server.check[:]) {
		cl.ch = &channel{inMAC: server.outMAC, outMAC: server.inMAC, inAES: server.outAES, outAES: server.inAES}
	}
	return reply
}

func startServer(t *testing.T, handle Handler) (*Server, string, *session.Store) {
	store, _ := session.Open("", time.Hour)
	srv := &Server{Sessions: store, Handle: handle, Log: slog.New(slog.DiscardHandler)}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Serve(ln)
	return srv, ln.Addr().String(), store
}

func dial(t *testing.T, addr string) *client {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &client{t: t, c: c}
}

func TestHandshakeAndTask(t *testing.T) {
	srv, addr, store := startServer(t, func(c *Conn, r *Request) Response {
		if r.Service == 12 && r.Task == 6 {
			return Response{Body: wire.Rows(r.Task, func(w *wire.Writer) uint32 { w.U32(1234); return 1 })}
		}
		return Response{}
	})
	sess, _ := store.Issue(&account.Player{PID: 7, BaasUserID: "abc", Nickname: "p"}, 5745)
	cl := dial(t, addr)
	if reply := cl.handshake(sess.Ticket, sess.LobbyKey()); cl.ch == nil {
		t.Fatalf("handshake refused: % x", reply)
	}

	req := append([]byte{12, wire.TU8, 6}, 0)
	f, _ := cl.ch.seal(innerTask, req)
	if _, err := cl.c.Write(f); err != nil {
		t.Fatal(err)
	}
	inner, payload, err := cl.ch.open(cl.readFrame())
	if err != nil || inner != innerReply {
		t.Fatal(inner, err)
	}
	r := wire.NewReader(payload)
	_, _ = r.U64()
	code, _ := r.U32()
	task, _ := r.U8()
	n, _ := r.U32()
	_, _ = r.U32()
	v, _ := r.U32()
	if code != 0 || task != 6 || n != 1 || v != 1234 {
		t.Fatal(code, task, n, v)
	}

	// An unknown task still gets an answer.
	f, _ = cl.ch.seal(innerTask, append([]byte{99, wire.TU8, 1}, 0))
	cl.c.Write(f)
	if _, payload, err = cl.ch.open(cl.readFrame()); err != nil || !bytes.Equal(payload[9:14], []byte{wire.TU32, 0, 0, 0, 0}) {
		t.Fatalf("unknown task: %v %x", err, payload)
	}

	// Keepalive in, keepalive out.
	cl.c.Write(make([]byte, 4))
	var ka [4]byte
	io.ReadFull(cl.c, ka[:])
	if ka != [4]byte{} {
		t.Fatalf("keepalive % x", ka)
	}
	if srv.Online() != 1 {
		t.Fatal("connection not registered")
	}
}

func TestHandshakeRefusals(t *testing.T) {
	_, addr, store := startServer(t, func(*Conn, *Request) Response { return Response{} })
	sess, _ := store.Issue(&account.Player{PID: 7, BaasUserID: "abc"}, 5745)

	// The all-zero key that anyone can compute.
	zero := append([]byte(nil), sess.Ticket...)
	copy(zero[97:121], make([]byte, 24))
	cl := dial(t, addr)
	if reply := cl.handshake(zero, make([]byte, 24)); reply[5] != msgAuthFail {
		t.Fatalf("zero-key ticket accepted: % x", reply)
	}

	// A genuine ticket with a tag made from another key.
	cl = dial(t, addr)
	if reply := cl.handshake(sess.Ticket, bytes.Repeat([]byte{9}, 24)); reply[5] != msgAuthFail {
		t.Fatalf("wrong key accepted: % x", reply)
	}

	// The key wrapped with the game's public key is the one other accepted form.
	cl = dial(t, addr)
	if cl.handshake(sess.Ticket, wrapped(sess.LobbyKey())); cl.ch == nil {
		t.Fatal("wrapped key refused")
	}
}
