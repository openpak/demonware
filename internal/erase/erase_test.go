package erase

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openpak/demonware/account"
	"github.com/openpak/demonware/session"
)

const (
	acctA = "11111111-1111-1111-1111-111111111111"
	acctB = "22222222-2222-2222-2222-222222222222"
)

type fakeLinks map[string][]string

func (f fakeLinks) SwitchSubjects(_ context.Context, id string) ([]string, error) { return f[id], nil }

func fixture(t *testing.T) (*Eraser, string, *session.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := session.Open(filepath.Join(dir, "sessions.json"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pa := &account.Player{PID: 1800000101, BaasUserID: "00000000000000aa"}
	pb := &account.Player{PID: 1800000102, BaasUserID: "00000000000000bb"}
	for _, p := range []*account.Player{pa, pb} {
		if _, err := st.Issue(p, 1); err != nil {
			t.Fatal(err)
		}
	}
	wd := filepath.Join(dir, "ctr-wallets")
	_ = os.MkdirAll(wd, 0o755)
	for _, f := range []string{"1800000101.json", "1800000102.json"} {
		_ = os.WriteFile(filepath.Join(wd, f), []byte(`{}`), 0o600)
	}
	e := &Eraser{Links: fakeLinks{acctA: {"00000000000000aa"}, acctB: {"00000000000000bb"}}, Sessions: st, WalletDir: wd}
	return e, wd, st
}

func TestEraseOnlyThatAccountAndIdempotent(t *testing.T) {
	e, wd, st := fixture(t)
	n, err := e.Erase(context.Background(), acctA)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := os.Stat(filepath.Join(wd, "1800000101.json")); !os.IsNotExist(err) {
		t.Fatal("wallet A kept")
	}
	if _, err := os.Stat(filepath.Join(wd, "1800000102.json")); err != nil {
		t.Fatal("wallet B gone")
	}
	pids, _ := st.PIDsFor(context.Background(), []string{"00000000000000aa", "00000000000000bb"})
	if len(pids) != 1 || pids[0] != 1800000102 {
		t.Fatalf("sessions left: %v", pids)
	}
	if n, err := e.Erase(context.Background(), acctA); err != nil || n != 0 {
		t.Fatalf("second run n=%d err=%v", n, err)
	}
}

func TestEraseUsesPIDLookupForWalletsWithoutSessions(t *testing.T) {
	e, wd, _ := fixture(t)
	_ = os.WriteFile(filepath.Join(wd, "1800000999.json"), []byte(`{}`), 0o600)
	e.Links = fakeLinks{acctA: {"00000000000000ff"}}
	e.PIDOf = func(_ context.Context, sub string) (uint64, error) { return 1800000999, nil }
	if n, _ := e.Erase(context.Background(), acctA); n != 1 {
		t.Fatalf("n=%d", n)
	}
}

func TestEndpointAuth(t *testing.T) {
	e, _, _ := fixture(t)
	call := func(key, hdr, id string) int {
		mux := http.NewServeMux()
		mux.HandleFunc("DELETE /internal/accounts/{account_id}/data", e.Handler(key))
		r := httptest.NewRequest("DELETE", "/internal/accounts/"+id+"/data", nil)
		if hdr != "" {
			r.Header.Set("Authorization", hdr)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	for _, c := range []struct {
		key, hdr, id string
		want         int
	}{
		{"", "Bearer k", acctA, 503}, {"k", "", acctA, 401}, {"k", "Bearer x", acctA, 401},
		{"k", "Bearer k", "nope", 400}, {"k", "Bearer k", acctA, 200},
	} {
		if got := call(c.key, c.hdr, c.id); got != c.want {
			t.Errorf("%+v: %d", c, got)
		}
	}
}
