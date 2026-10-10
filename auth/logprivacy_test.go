package auth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openpak/demonware/account"
	"github.com/openpak/demonware/session"
)

type sentinelResolver struct{}

func (sentinelResolver) Resolve(tok string) (*account.Player, error) {
	switch tok {
	case "good":
		return &account.Player{PID: 7788991122, BaasUserID: "00000000000000aa", Nickname: "Zorblaxa"}, nil
	case "banned":
		return nil, account.ErrBanned
	}
	// What account.Client returns when nx-baas cannot be reached: the URL and both addresses.
	return nil, &url.Error{Op: "Post", URL: "http://nx.internal:8080/internal/switch/identity?key=secret-key-77",
		Err: &net.OpError{Op: "read", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 5151}, Err: errors.New("i/o timeout")}}
}

// Login, refusal and umbrella lines must carry no pid, nickname, token, address or URL.
func TestLogsCarryNoPersonalData(t *testing.T) {
	var out bytes.Buffer
	store, _ := session.Open("", time.Hour)
	s := &Service{
		Accounts: sentinelResolver{}, Sessions: store, Titles: map[uint32]bool{5745: true, 5775: true},
		Log: slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	const remote = "203.0.113.77:44312"
	post := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	loginBody := func(title, token string) string {
		extra, _ := json.Marshal(map[string]string{"version": "1", "token": token, "username": "Zorblaxa"})
		b, _ := json.Marshal(map[string]string{"auth_task": "78", "iv_seed": "123", "title_id": title, "extra_data": string(extra)})
		return string(b)
	}

	w := post("/auth/", loginBody("5745", "good"))
	var reply loginReply
	_ = json.Unmarshal(w.Body.Bytes(), &reply)
	post("/auth/", loginBody("5745", "banned"))
	post("/auth/", loginBody("5745", "unreachable-token-xyz"))
	s.Open = true
	post("/auth/", loginBody("5745", "unreachable-token-xyz"))
	s.Open = false

	umb := func(ticket string) {
		b, _ := json.Marshal(map[string]any{"ticket": ticket, "titleID": "5745"})
		post("/v1.0/tokens/lsg/", string(b))
	}
	umb(reply.ClientTicket)
	umb(base64.StdEncoding.EncodeToString([]byte("not a ticket")))
	post("/v1.0/users/7788991122/Zorblaxa/tokens/?client=x", "{}")

	got := out.String()
	for _, want := range []string{"auth ok", "auth refused", "umbrella ok", "umbrella refused", "auth: unhandled request", "pid=~"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in log:\n%s", want, got)
		}
	}
	for _, secret := range []string{
		"7788991122", "Zorblaxa", "203.0.113", "44312", "5151", "unreachable-token-xyz", "secret-key-77", "nx.internal", "good", reply.ClientTicket,
	} {
		if strings.Contains(got, secret) {
			t.Errorf("log contains %q:\n%s", secret, got)
		}
	}
}
