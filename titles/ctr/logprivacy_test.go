package ctr

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openpak/demonware/account"
	"github.com/openpak/demonware/lobby"
	"github.com/openpak/demonware/session"
	"github.com/openpak/demonware/wire"
)

func request(svc, task byte, args func(w *wire.Writer)) *lobby.Request {
	w := &wire.Writer{}
	w.B = []byte{svc}
	w.U8(task)
	if args != nil {
		args(w)
	}
	w.B = append(w.B, 0)
	r := wire.NewReader(w.B)
	r.Off = 1
	_, _ = r.U8()
	return &lobby.Request{Service: svc, Task: task, Args: r, Raw: w.B}
}

// Wallet errors, lobby and search ids and push failures must reach the log as tags, not as the
// player's pid or the ids the game sent.
func TestLogsCarryNoPersonalData(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	lob := &lobby.Server{Log: log}
	_, tt, err := New(t.TempDir(), 100, log)
	if err != nil {
		t.Fatal(err)
	}
	const pid = 7788991122
	const lobbyID = 424242424242
	c := lob.Fake(&session.Session{Title: 5775, Expires: time.Now().Add(time.Hour),
		Player: account.Player{PID: pid, BaasUserID: strconv.FormatUint(pid, 16), Nickname: "Zorblaxa"}}, "203.0.113.77")

	// A damaged wallet file: the error names the wallet's owner.
	if err := os.WriteFile(tt.Wallets.path(pid), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	tt.market(c, request(svcMarketplace, 245, nil))

	// Lobby documents and searches by id.
	b64 := func(n int) string { return base64.StdEncoding.EncodeToString(make([]byte, n)) }
	info := fmt.Sprintf(`{"listen_server":{"local_address":%q,"security_id":%q,"security_key":%q,"nat":1}}`, b64(67), b64(8), b64(16))
	tt.async(c, request(svcAsyncMM, 2, func(w *wire.Writer) { w.String(info) }))
	tt.async(c, request(svcAsyncMM, 15, func(w *wire.Writer) { w.U64(lobbyID).U64(1).String("{}") }))
	search := fmt.Sprintf(`{"members":[%d],"lobby_slots":{"min":1,"max":8},"ruleset_payload":{"filter":{"mode":"race"}}}`, c.OnlineID())
	resp := tt.async(c, request(svcAsyncMM, 18, func(w *wire.Writer) { w.String(search) }))
	if resp.Then != nil {
		resp.Then()
	}
	tt.async(c, request(svcAsyncMM, 18, func(w *wire.Writer) { w.String(`{"members":[]}`) }))

	got := out.String()
	for _, want := range []string{"ctr: wallet", "ctr: sync", "ctr: searching", "ctr: search rejected", "lobby=~", "search=~"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in log:\n%s", want, got)
		}
	}
	for _, secret := range []string{"7788991122", strconv.Itoa(lobbyID), strconv.FormatUint(c.OnlineID(), 10), "Zorblaxa", "203.0.113.77", "198.51.100"} {
		if strings.Contains(got, secret) {
			t.Errorf("log contains %q:\n%s", secret, got)
		}
	}
}
