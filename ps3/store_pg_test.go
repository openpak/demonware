package ps3

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DEMONWARE_TEST_PG=postgres://... runs the store against a real Postgres.
func TestStorePostgres(t *testing.T) {
	url := os.Getenv("DEMONWARE_TEST_PG")
	if url == "" {
		t.Skip("DEMONWARE_TEST_PG unset")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := NewStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(ctx, db); err != nil { // idempotent
		t.Fatal(err)
	}
	acct := "aaaaaaaa-0000-0000-0000-000000000001"
	pid := PID(acct)
	ticket := []byte("a-ticket-of-128-bytes")
	if err := s.PutSession(ctx, ticket, Session{PID: pid, AccountID: acct, OnlineID: "P", Title: 7, Issued: time.Now(), Expires: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Session(ctx, ticket)
	if err != nil || !ok || got.PID != pid || got.Title != 7 {
		t.Fatalf("session: %+v %v %v", got, ok, err)
	}
	if _, ok, _ := s.Session(ctx, []byte("other")); ok {
		t.Fatal("unknown ticket found")
	}
	if err := s.PutFile(ctx, 7, pid, "mpdata", []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFile(ctx, 7, pid, "mpdata", []byte{4, 5}); err != nil {
		t.Fatal(err)
	}
	if d, ok, _ := s.File(ctx, 7, pid, "mpdata"); !ok || len(d) != 2 {
		t.Fatalf("file %v %v", d, ok)
	}
	if err := s.PutGame(ctx, Game{ID: []byte("12345678"), Title: 7, PID: pid, Info: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if s.Games(ctx) < 1 {
		t.Fatal("games")
	}
	if n, err := s.DeleteGames(ctx, pid, []byte("12345678")); err != nil || n != 1 {
		t.Fatalf("delete %d %v", n, err)
	}
	_ = s.PutGame(ctx, Game{ID: []byte("87654321"), Title: 7, PID: pid, Info: []byte("x")})
	n, err := s.Erase(ctx, acct)
	if err != nil || n != 3 { // session, file, game
		t.Fatalf("erase %d %v", n, err)
	}
	if _, ok, _ := s.File(ctx, 7, pid, "mpdata"); ok {
		t.Fatal("file survived erasure")
	}
}

func TestStoreMemoryErase(t *testing.T) {
	ctx := context.Background()
	s, _ := NewStore(ctx, nil)
	acct := "bbbbbbbb-0000-0000-0000-000000000002"
	pid := PID(acct)
	_ = s.PutSession(ctx, []byte("t"), Session{PID: pid, AccountID: acct, Expires: time.Now().Add(time.Hour)})
	_ = s.PutFile(ctx, 1, pid, "f", []byte{1})
	_ = s.PutGame(ctx, Game{ID: []byte("g"), PID: pid})
	_ = s.PutFile(ctx, 1, pid+1, "f", []byte{1}) // someone else's
	if n, _ := s.Erase(ctx, acct); n != 3 {
		t.Fatalf("erased %d", n)
	}
	if _, ok, _ := s.File(ctx, 1, pid+1, "f"); !ok {
		t.Fatal("erased another player's file")
	}
}
