package ctr

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestWalletsSurviveRestartInPostgres imports legacy files once and keeps new state; it needs
// TEST_DATABASE_URL.
func TestWalletsSurviveRestartInPostgres(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "42.json"), []byte(`{"balance":{"10":777},"items":{"5":true}}`), 0o600)

	w1 := &Wallets{Dir: dir, Start: 100, DB: pool}
	if _, err := pool.Exec(ctx, walletSchema); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM ctr_wallets WHERE pid IN (42, 43)`)
	if n, err := w1.openDB(ctx); err != nil || n != 1 {
		t.Fatalf("import: %d %v", n, err)
	}
	if err := w1.update(43, func(w *wallet) error { w.Balance[coins] -= 30; return nil }); err != nil {
		t.Fatal(err)
	}
	// New instance on the same DB: the import does not repeat, both wallets are there.
	w2 := &Wallets{Dir: dir, Start: 100, DB: pool}
	if n, err := w2.openDB(ctx); err != nil || n != 0 {
		t.Fatalf("second import: %d %v", n, err)
	}
	a, _ := w2.view(42)
	b, _ := w2.view(43)
	if a.Balance[coins] != 777 || !a.Items[5] || b.Balance[coins] != 70 {
		t.Fatalf("after restart: %+v %+v", a, b)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM ctr_wallets WHERE pid IN (42, 43)`)
}
