package account

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func jwt(claims map[string]any) string {
	p, _ := json.Marshal(claims)
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(p) + ".c2ln"
}

func TestResolve(t *testing.T) {
	var got []map[string]any
	nx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/switch/identity" || r.Header.Get("X-Internal-Key") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var q map[string]any
		json.NewDecoder(r.Body).Decode(&q)
		got = append(got, q)
		if q["pid"] == float64(666) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"pid": 1800000101, "baas_user_id": "0123456789abcdef",
			"nickname": "hansolo", "friends": []map[string]any{{"pid": 1800000102, "baas_user_id": "aa"}}})
	}))
	defer nx.Close()
	c := New(nx.URL, "k")

	// An emulator's token carries openpak: that claim is what nx-baas proves.
	p, err := c.Resolve(jwt(map[string]any{"openpak": "nx2.abc.def", "sub": "x"}))
	if err != nil || p.PID != 1800000101 || p.OnlineID() != 0x0123456789abcdef || len(p.Friends) != 1 {
		t.Fatal(p, err)
	}
	if got[0]["openpak"] != "nx2.abc.def" {
		t.Fatalf("sent %v", got[0])
	}
	// A retail console's token has no openpak: the whole token goes.
	tok := jwt(map[string]any{"sub": "x", "typ": "id_token"})
	if _, err := c.Resolve(tok); err != nil || got[1]["id_token"] != tok {
		t.Fatalf("sent %v (%v)", got[1], err)
	}
	if _, err := c.Resolve("not-a-token"); !errors.Is(err, ErrUnknown) {
		t.Fatal("garbage token", err)
	}
	if err := c.Check(666); !errors.Is(err, ErrBanned) {
		t.Fatal("ban not reported", err)
	}
	// An active answer is cached: the second check does not call nx-baas.
	n := len(got)
	if c.Check(1800000101) != nil || len(got) != n {
		t.Fatal("active answer not cached")
	}
}

func TestResolveFailsClosed(t *testing.T) {
	c := New("http://127.0.0.1:1", "k")
	if _, err := c.Resolve(jwt(map[string]any{"openpak": "x"})); err == nil {
		t.Fatal("an unreachable nx-baas proved a player")
	}
}
