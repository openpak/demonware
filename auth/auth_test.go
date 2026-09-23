package auth

import (
	"bufio"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openpak/demonware/account"
	"github.com/openpak/demonware/session"
)

type fakeResolver map[string]error

func (f fakeResolver) Resolve(tok string) (*account.Player, error) {
	if err, ok := f[tok]; ok && err != nil {
		return nil, err
	}
	if _, ok := f[tok]; !ok {
		return nil, account.ErrUnknown
	}
	return &account.Player{PID: 1800000101, BaasUserID: "00000000000000aa", Nickname: "hansolo"}, nil
}

func service(t *testing.T) *Service {
	store, _ := session.Open("", time.Hour)
	return &Service{
		Accounts: fakeResolver{"good": nil, "banned": account.ErrBanned},
		Sessions: store, Titles: map[uint32]bool{5745: true, 5775: true},
		Log: slog.New(slog.DiscardHandler),
	}
}

func login(s *Service, title, token string) *httptest.ResponseRecorder {
	extra, _ := json.Marshal(map[string]string{"version": "1", "token": token, "username": "hansolo"})
	body, _ := json.Marshal(map[string]string{"auth_task": "78", "iv_seed": "123", "title_id": title,
		"identity": "356c4bc3", "extra_data": string(extra)})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/auth/", strings.NewReader(string(body))))
	return w
}

func TestLogin(t *testing.T) {
	s := service(t)
	w := login(s, "5745", "good")
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	// The client parses the reply in this order.
	last := -1
	for _, k := range []string{"auth_task", "code", "iv_seed", "client_ticket", "server_ticket", "extra_data"} {
		i := strings.Index(body, `"`+k+`"`)
		if i < last {
			t.Fatalf("%s out of order in %s", k, body)
		}
		last = i
	}
	var r loginReply
	json.Unmarshal(w.Body.Bytes(), &r)
	if r.AuthTask != "79" || r.Code != "700" || r.IVSeed != "123" {
		t.Fatalf("%+v", r)
	}
	tk, err := base64.StdEncoding.DecodeString(r.ClientTicket)
	if err != nil || len(tk) != 128 || s.Sessions.Find(tk) == nil {
		t.Fatal("ticket not issued")
	}
	var extra map[string]string
	if json.Unmarshal([]byte(r.ExtraData), &extra) != nil || extra["nso_subscription_status"] != "1" || extra["extended_data"] == "" {
		t.Fatalf("extra_data %q", r.ExtraData)
	}
}

func TestLoginRefusals(t *testing.T) {
	s := service(t)
	for name, c := range map[string]struct{ title, token string }{
		"banned":         {"5745", "banned"},
		"unknown":        {"5745", "stranger"},
		"no token":       {"5745", ""},
		"unserved title": {"1234", "good"},
	} {
		if w := login(s, c.title, c.token); w.Code != http.StatusForbidden {
			t.Errorf("%s: status %d", name, w.Code)
		}
	}
	// Open mode (local testing) admits an unknown token but never a banned one.
	s.Open = true
	if w := login(s, "5745", "stranger"); w.Code != 200 {
		t.Errorf("open mode refused an unknown token: %d", w.Code)
	}
	if w := login(s, "5745", "banned"); w.Code != http.StatusForbidden {
		t.Errorf("open mode admitted a banned account: %d", w.Code)
	}
}

func TestUmbrella(t *testing.T) {
	s := service(t)
	var r loginReply
	json.Unmarshal(login(s, "5775", "good").Body.Bytes(), &r)
	call := func(ticket string, title any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"ticket": ticket, "titleID": title})
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("POST", "/v1.0/tokens/lsg/", strings.NewReader(string(body))))
		return w
	}
	for _, title := range []any{5775, "5775"} {
		w := call(r.ClientTicket, title)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != 200 || out["accessToken"] == "" || out["accounts"] == nil {
			t.Fatalf("title %v: %d %s", title, w.Code, w.Body)
		}
	}
	if w := call(r.ClientTicket, 5745); w.Code != 401 {
		t.Fatal("ticket accepted for another title")
	}
	forged := make([]byte, 128)
	copy(forged, []byte{0xDE, 0xAD, 0xBD, 0xEF})
	if w := call(base64.StdEncoding.EncodeToString(forged), 5775); w.Code != 401 {
		t.Fatal("forged ticket accepted")
	}
}

func TestSignerZeroSalt(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"auth_task":"79"}`)
	sig64, err := s.Sign(body)
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := base64.StdEncoding.DecodeString(sig64)
	digest := sha256.Sum256(body)
	if err := rsa.VerifyPSS(&key.PublicKey, crypto.SHA256, digest[:], sig, nil); err != nil {
		t.Fatal(err)
	}
	// Recover the encoded message and check the salt is empty: DB ends 0x01 with nothing after.
	em := new(big.Int).Exp(new(big.Int).SetBytes(sig), big.NewInt(int64(key.E)), key.N).FillBytes(make([]byte, 256))
	H := em[256-33 : 256-1]
	db := em[:256-33]
	mask := mgf1(H, len(db))
	for i := range db {
		db[i] ^= mask[i]
	}
	db[0] &= 0x7F
	if db[len(db)-1] != 0x01 {
		t.Fatalf("salt is not empty: DB ends %x", db[len(db)-4:])
	}
	for _, b := range db[:len(db)-1] {
		if b != 0 {
			t.Fatal("padding is not zero")
		}
	}
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	if _, err := NewSigner(small); err == nil {
		t.Fatal("a 1024-bit key was accepted")
	}
}

func TestProxyProtocol(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	pl := ProxyListener{ln}
	go func() {
		c, _ := net.Dial("tcp", ln.Addr().String())
		c.Write([]byte("PROXY TCP4 203.0.113.9 10.0.0.1 51234 443\r\nhello"))
		c.Close()
	}()
	c, err := pl.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if got := c.RemoteAddr().String(); got != "203.0.113.9:51234" {
		t.Fatalf("remote %s", got)
	}
	if b, _ := io.ReadAll(bufio.NewReader(c)); string(b) != "hello" {
		t.Fatalf("payload %q", b)
	}

	go func() {
		c, _ := net.Dial("tcp", ln.Addr().String())
		c.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
		time.Sleep(100 * time.Millisecond)
		c.Close()
	}()
	c, _ = pl.Accept()
	if _, err := c.Read(make([]byte, 10)); err == nil {
		t.Fatal("a connection without a PROXY header was served")
	}
}
