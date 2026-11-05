package ps3

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openpak/demonware/wire"
)

// issue mirrors np-common ticket.Issue (envelope, extension, signature); cookie, when set, is an
// extra 24-byte field after the twelve firmware fields (np-common v0.3.0).
func issue(t *testing.T, priv ed25519.PrivateKey, id Identity, cookie []byte) []byte {
	t.Helper()
	elem := func(tag uint16, v []byte) []byte {
		n := 4
		if tag == 0x3000 || tag == 0x3010 {
			n = 6
		}
		b := make([]byte, n+len(v))
		binary.BigEndian.PutUint16(b, tag)
		if n == 6 {
			binary.BigEndian.PutUint32(b[2:], uint32(len(v)))
		} else {
			binary.BigEndian.PutUint16(b[2:], uint16(len(v)))
		}
		copy(b[n:], v)
		return b
	}
	fixed := func(s string, n int) []byte { b := make([]byte, n); copy(b, s); return b }
	vals := [][]byte{make([]byte, 20), make([]byte, 4), make([]byte, 8), make([]byte, 8), make([]byte, 8),
		fixed(id.OnlineID, 32), fixed("PT", 4), fixed("un", 4), fixed(id.Service, 24), make([]byte, 4), make([]byte, 4), make([]byte, 8)}
	tags := []uint16{8, 1, 7, 7, 2, 4, 8, 4, 8, 0x3011, 1, 7}
	var body []byte
	for i, v := range vals {
		body = append(body, elem(tags[i], v)...)
	}
	if cookie != nil { // np-common v0.3.0 puts it after the twelve firmware fields
		body = append(body, elem(8, cookie)...)
	}
	out := append([]byte{0x40, 0, 0, 0}, elem(0x3000, body)...)
	out = append(out, elem(0x3010, nil)...)
	out = append(out, elem(8, make([]byte, 8))...)
	out = append(out, elem(8, make([]byte, 64))...)
	claims, _ := json.Marshal(id)
	payload := append(append([]byte{}, opkt...), claims...)
	signed := append(append([]byte("openpak-np-ticket-v1\x00"), out...), payload...)
	payload = append(payload, ed25519.Sign(priv, signed)...)
	return append(out, elem(0x3012, payload)...)
}

type fakeAccounts struct{ banned map[string]bool }

func (f fakeAccounts) Active(_ context.Context, id string) error {
	if f.banned[id] {
		return ErrBanned
	}
	return nil
}

type recRewards struct {
	mu  sync.Mutex
	log []string
}

func (r *recRewards) add(s string)                               { r.mu.Lock(); r.log = append(r.log, s); r.mu.Unlock() }
func (r *recRewards) Hosted(title uint32, game []byte, a string) { r.add("hosted " + a) }
func (r *recRewards) Ended(game []byte, a string)                { r.add("ended " + a) }
func (r *recRewards) Left(a string)                              { r.add("left " + a) }

type logbuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logbuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logbuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func startServer(t *testing.T, banned ...string) (addr string, pub ed25519.PublicKey, priv ed25519.PrivateKey, srv *Server, rw *recRewards, logs *logbuf, store *Store) {
	t.Helper()
	pub, priv, _ = ed25519.GenerateKey(rand.Reader)
	store, _ = NewStore(context.Background(), nil)
	logs = &logbuf{}
	rw = &recRewards{}
	b := map[string]bool{}
	for _, x := range banned {
		b[x] = true
	}
	var err error
	srv, err = New(Config{Store: store, PublicKey: pub, Secret: bytes.Repeat([]byte{7}, 32), PublicIP: net.IPv4(203, 0, 113, 9),
		Accounts: fakeAccounts{b}, Rewards: rw, Log: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	sw := Split(ln, srv.Handle)
	go func() { // a stand-in Switch lobby: echoes its first byte
		for {
			c, err := sw.Accept()
			if err != nil {
				return
			}
			go func() { b := make([]byte, 28); io.ReadFull(c, b); c.Write([]byte{'S'}); c.Close() }()
		}
	}()
	return ln.Addr().String(), pub, priv, srv, rw, logs, store
}

func readFrameT(t *testing.T, c net.Conn) (byte, byte, []byte) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var lb [4]byte
	if _, err := io.ReadFull(c, lb[:]); err != nil {
		t.Fatalf("read: %v", err)
	}
	body := make([]byte, binary.LittleEndian.Uint32(lb[:]))
	io.ReadFull(c, body)
	return body[0], body[1], body[2:]
}

func decryptPlain(t *testing.T, key, payload []byte) []byte {
	t.Helper()
	plain, err := open(key, payload)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(plain) != 0xDEADBEEF {
		t.Fatalf("plain does not start with the magic: %x", plain[:8])
	}
	return plain
}

// clientTask sends one encrypted task [u32 hash][svc][typed u8 task][args] and returns the plain reply body.
func clientTask(t *testing.T, c net.Conn, key []byte, svc byte, task byte, args []byte) (byte, []byte) {
	t.Helper()
	plain := append([]byte{0, 0, 0, 0, svc, wire.TU8, task}, args...)
	plain = append(plain, 0)
	for len(plain)%8 != 0 {
		plain = append(plain, 0)
	}
	seed := uint32(0x1234)
	mode, _ := newCBC(key, IV(seed), true)
	ct := make([]byte, len(plain))
	mode.CryptBlocks(ct, plain)
	f := append([]byte{1}, binary.LittleEndian.AppendUint32(nil, seed)...)
	f = append(f, ct...)
	c.Write(append(binary.LittleEndian.AppendUint32(nil, uint32(len(f))), f...))
	enc, typ, p := readFrameT(t, c)
	if enc != 1 {
		t.Fatalf("reply not encrypted")
	}
	// readFrame split off the first byte after the flag, as the server does
	body := decryptPlain(t, key, append([]byte{typ}, p...))
	return body[4], body[5:]
}

func TestMW3AuthLobbyStorageAndGames(t *testing.T) {
	addr, _, priv, srv, rw, logs, store := startServer(t)
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	now := time.Now()
	id := Identity{AccountID: "11111111-2222-3333-4444-555555555555", LinkID: "L", OnlineID: "Player_One", Country: "PT", Service: "IV0002-BLES01428_00",
		Issued: now.UnixMilli(), Expires: now.Add(time.Hour).UnixMilli()}
	cookie := []byte("0123456789abcdefghijklmn")
	tk := issue(t, priv, id, cookie)

	// Auth: message 18 = bit buffer {typed u32 title}, then the ticket bytes.
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := newBitWriter()
	w.u32(5555)
	w.raw(0, 2) // the ticket is byte aligned in the real request
	w.bytes(tk)
	c.Write(frame(msgAuthRequest, w.out()))
	enc, typ, p := readFrameT(t, c)
	if enc != 0 || typ != msgAuthReply {
		t.Fatalf("auth reply enc=%d type=%d", enc, typ)
	}
	r := newBitReader(p)
	if code := r.u32(); code != 700 {
		t.Fatalf("auth code %d", code)
	}
	seed := r.u32()
	encTicket := make([]byte, 152)
	for i := range encTicket {
		encTicket[i] = byte(r.raw(8))
	}
	serverTicket := make([]byte, 128)
	for i := range serverTicket {
		serverTicket[i] = byte(r.raw(8))
	}
	mode, _ := newCBC(cookie, IV(seed), false)
	plain := make([]byte, 152)
	mode.CryptBlocks(plain, encTicket)
	if binary.LittleEndian.Uint32(plain) != 0xEFBDADDE || binary.LittleEndian.Uint32(plain[5:]) != 5555 {
		t.Fatalf("bad bdAuthTicket %x", plain[:16])
	}
	pid := PID(id.AccountID)
	if binary.LittleEndian.Uint64(plain[25:]) != pid || !strings.HasPrefix(string(plain[33:]), "Player_One") {
		t.Fatalf("bdAuthTicket player wrong")
	}
	key := plain[97:121]
	if !bytes.Equal(key, srv.sessionKey(serverTicket)) {
		t.Fatalf("session key differs from the derived one")
	}

	// Lobby: new connection, header, login with the server ticket at a non-byte-aligned offset.
	l, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Write([]byte{0xc8, 0, 0, 0, 0xff, 0xff, 0, 0})
	lw := newBitWriter()
	lw.u32(0xdead) // junk before the ticket moves it off a byte boundary
	lw.raw(1, 3)
	lw.bytes(serverTicket)
	l.Write(frame(msgLogin, lw.out()))
	_, typ, p = readFrameT(t, l)
	connPlain := decryptPlain(t, key, append([]byte{typ}, p...))
	if connPlain[4] != msgConnID || connPlain[5] != wire.TU64 {
		t.Fatalf("expected a connection id, got %x", connPlain[:12])
	}

	// Publisher file by name.
	svc, body := clientTask(t, l, key, svcStorage, 7, (&wire.Writer{}).String("playlists_tu24.aggr").B)
	// body = message type 1, then [u64 txn][u32 err][u8 task][u32 count][u32 total][blob]
	if svc != msgTaskReply {
		t.Fatalf("not a task reply: %d", svc)
	}
	rep := body
	if rep[0] != wire.TU64 || binary.LittleEndian.Uint32(rep[10:]) != 0 {
		t.Fatalf("task error: %x", rep[:20])
	}
	if !bytes.Contains(body, []byte{0x78}) { // zlib header byte inside the blob
		t.Fatalf("no playlist blob in reply")
	}
	// Missing publisher file is "no such file" (1000).
	_, body = clientTask(t, l, key, svcStorage, 7, (&wire.Writer{}).String("nope").B)
	if binary.LittleEndian.Uint32(body[10:]) != bdNoFile {
		t.Fatalf("want error 1000, got %x", body[:20])
	}

	// User file: read before write is "no such file", then store and read back.
	_, body = clientTask(t, l, key, svcStorage, 3, (&wire.Writer{}).String("mpdata").B)
	if binary.LittleEndian.Uint32(body[10:]) != bdNoFile {
		t.Fatalf("new player should get 1000")
	}
	data := bytes.Repeat([]byte{0xAB}, 300)
	clientTask(t, l, key, svcStorage, 2, (&wire.Writer{}).String("mpdata").Blob(data).B)
	got, ok, _ := store.File(context.Background(), 5555, pid, "mpdata")
	if !ok || !bytes.Equal(got, data) {
		t.Fatalf("mpdata not stored")
	}
	_, body = clientTask(t, l, key, svcStorage, 3, (&wire.Writer{}).String("mpdata").B)
	if !bytes.Contains(body, data) {
		t.Fatalf("mpdata not returned")
	}

	// Matchmaking: create, then delete.
	_, body = clientTask(t, l, key, svcMatchmaker, 1, (&wire.Writer{}).U8(0).Blob([]byte("addr")).B)
	if store.Games(context.Background()) != 1 {
		t.Fatalf("game not stored")
	}
	gid := body[32:40]
	clientTask(t, l, key, svcMatchmaker, 3, (&wire.Writer{}).Blob(gid).B)
	if store.Games(context.Background()) != 0 {
		t.Fatalf("game not removed")
	}

	// Bandwidth test comes back as a raw message 5.
	{
		plain := []byte{0, 0, 0, 0, svcBandwidth, 0, 0, 0}
		for len(plain)%8 != 0 {
			plain = append(plain, 0)
		}
		mode, _ := newCBC(key, IV(1), true)
		ct := make([]byte, len(plain))
		mode.CryptBlocks(ct, plain)
		f := append([]byte{1}, binary.LittleEndian.AppendUint32(nil, 1)...)
		f = append(f, ct...)
		l.Write(append(binary.LittleEndian.AppendUint32(nil, uint32(len(f))), f...))
		_, typ, p := readFrameT(t, l)
		b := decryptPlain(t, key, append([]byte{typ}, p...))
		if b[4] != msgRawReply || !bytes.Contains(b, []byte("BWTEST01")) || !bytes.Contains(b, []byte{203, 0, 113, 9}) {
			t.Fatalf("bandwidth reply %x", b[:40])
		}
	}

	// Close: hosted game removed, rewards told.
	clientTask(t, l, key, svcMatchmaker, 1, (&wire.Writer{}).U8(0).Blob([]byte("addr2")).B)
	l.Close()
	deadline := time.Now().Add(3 * time.Second)
	for store.Games(context.Background()) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if store.Games(context.Background()) != 0 {
		t.Fatalf("hosted game survived the disconnect")
	}
	time.Sleep(50 * time.Millisecond)
	rw.mu.Lock()
	if len(rw.log) < 3 || rw.log[len(rw.log)-1] != "left "+id.OnlineID {
		t.Fatalf("rewards calls: %v", rw.log)
	}
	rw.mu.Unlock()
	if srv.Logins.Load() != 1 {
		t.Fatalf("logins %d", srv.Logins.Load())
	}

	// Logs: requests are logged, secrets never.
	out := logs.String()
	for _, secret := range []string{string(cookie), id.AccountID, "Player_One", string(data[:4])} {
		if strings.Contains(out, secret) {
			t.Errorf("log leaks %q", secret)
		}
	}
	if !strings.Contains(out, "lobby task") || !strings.Contains(out, "auth ok") {
		t.Errorf("requests are not logged:\n%s", out)
	}
}

func TestRefusals(t *testing.T) {
	addr, _, priv, srv, _, _, _ := startServer(t, "99999999-0000-0000-0000-000000000000")
	now := time.Now()
	mk := func(acct string, cookie []byte, exp time.Time, key ed25519.PrivateKey) []byte {
		return issue(t, key, Identity{AccountID: acct, LinkID: "L", OnlineID: "P", Country: "PT", Service: "s",
			Issued: now.UnixMilli(), Expires: exp.UnixMilli()}, cookie)
	}
	other := func() ed25519.PrivateKey { _, k, _ := ed25519.GenerateKey(rand.Reader); return k }()
	cookie := []byte("0123456789abcdefghijklmn")
	for name, tk := range map[string][]byte{
		"banned":    mk("99999999-0000-0000-0000-000000000000", cookie, now.Add(time.Hour), priv),
		"no cookie": mk("a", nil, now.Add(time.Hour), priv),
		"expired":   mk("a", cookie, now.Add(-time.Minute), priv),
		"forged":    mk("a", cookie, now.Add(time.Hour), other),
	} {
		c, _ := net.Dial("tcp", addr)
		w := newBitWriter()
		w.u32(1)
		w.raw(0, 2)
		w.bytes(tk)
		c.Write(frame(msgAuthRequest, w.out()))
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if n, _ := c.Read(make([]byte, 16)); n != 0 {
			t.Errorf("%s: got a reply", name)
		}
		c.Close()
	}
	if srv.Refused.Load() != 4 {
		t.Errorf("refused = %d, want 4", srv.Refused.Load())
	}
	// Lobby login with an unknown ticket.
	l, _ := net.Dial("tcp", addr)
	l.Write([]byte{0xc8, 0, 0, 0, 0xff, 0xff, 0, 0})
	lw := newBitWriter()
	t2 := make([]byte, 128)
	binary.LittleEndian.PutUint32(t2, serverTicketMagic)
	lw.bytes(t2)
	l.Write(frame(msgLogin, lw.out()))
	l.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, _ := l.Read(make([]byte, 16)); n != 0 {
		t.Errorf("unknown lobby ticket got a reply")
	}
}

func TestSplitRoutesSwitchAndPS3(t *testing.T) {
	addr, _, _, _, _, _, _ := startServer(t)
	hello := make([]byte, 28)
	for i, v := range []uint32{200, 200, 210, 220, 0x1FFFFF} {
		binary.LittleEndian.PutUint32(hello[i*4:], v)
	}
	c, _ := net.Dial("tcp", addr)
	c.Write(hello)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	b := make([]byte, 1)
	if _, err := io.ReadFull(c, b); err != nil || b[0] != 'S' {
		t.Fatalf("Switch hello did not reach the Switch lobby: %v %q", err, b)
	}
	if !IsSwitch(hello) || IsSwitch([]byte{0xc8, 0, 0, 0, 0xff, 0xff, 0, 0}) || IsSwitch([]byte{0x60, 2, 0, 0, 0, 0x12, 1, 2}) {
		t.Fatal("IsSwitch")
	}
}

func TestTicketParseWithoutCookieKeepsIdentity(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	tk := issue(t, priv, Identity{AccountID: "a", LinkID: "l", OnlineID: "P", Country: "PT", Service: "s",
		Issued: now.UnixMilli(), Expires: now.Add(time.Hour).UnixMilli()}, nil)
	got, err := ParseTicket(append([]byte("junk"), tk...), pub, now)
	if err != nil || got.AccountID != "a" || got.Cookie != nil {
		t.Fatalf("%v %+v", err, got)
	}
	tk[40] ^= 1 // tamper with the envelope
	if _, err := ParseTicket(tk, pub, now); err == nil {
		t.Fatal("tampered ticket accepted")
	}
}

func TestPublisherFilesAreValid(t *testing.T) {
	f := defaultFiles()
	a := f["playlists_tu24.aggr"]
	if len(a) == 0 || len(a) > 0xF000 || binary.BigEndian.Uint16(a) != 1 || binary.BigEndian.Uint16(a[2:]) != 6 {
		t.Fatalf("bad aggr header % x", a[:6])
	}
	if int(binary.BigEndian.Uint16(a[4:])) != len(a)-6 {
		t.Fatalf("aggr size field wrong")
	}
}
