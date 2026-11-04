package ctr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Wallets live in Postgres, one row per OpenPak account. The old ctr-wallets/<pid>.json files are
// imported once at start (rows already there win) and left in place.

const walletSchema = `
CREATE TABLE IF NOT EXISTS ctr_wallets (
  pid        bigint PRIMARY KEY,
  data       jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);`

// openDB creates the table and imports the legacy files; it returns how many rows it added.
func (s *Wallets) openDB(ctx context.Context) (int, error) {
	if _, err := s.DB.Exec(ctx, walletSchema); err != nil {
		return 0, err
	}
	files, _ := filepath.Glob(filepath.Join(s.Dir, "*.json"))
	n := 0
	for _, f := range files {
		pid, err := strconv.ParseUint(strings.TrimSuffix(filepath.Base(f), ".json"), 10, 64)
		if err != nil || pid == 0 {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			return n, err
		}
		tag, err := s.DB.Exec(ctx, `INSERT INTO ctr_wallets (pid, data) VALUES ($1, $2) ON CONFLICT (pid) DO NOTHING`, int64(pid), string(raw))
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

func (s *Wallets) dbRead(pid uint64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var raw string
	err := s.DB.QueryRow(ctx, `SELECT data::text FROM ctr_wallets WHERE pid = $1`, int64(pid)).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, os.ErrNotExist
	}
	return []byte(raw), err
}

func (s *Wallets) dbWrite(pid uint64, raw []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.DB.Exec(ctx, `INSERT INTO ctr_wallets (pid, data) VALUES ($1, $2)
		ON CONFLICT (pid) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`, int64(pid), string(raw))
	return err
}

// EraseWallets deletes the wallets of pids from the database (db non-nil) or from dir, and
// reports how many existed.
func EraseWallets(ctx context.Context, db *pgxpool.Pool, dir string, pids []uint64) (int, error) {
	n := 0
	if db != nil {
		ip := make([]int64, 0, len(pids))
		for _, p := range pids {
			ip = append(ip, int64(p))
		}
		tag, err := db.Exec(ctx, `DELETE FROM ctr_wallets WHERE pid = ANY($1)`, ip)
		return int(tag.RowsAffected()), err
	}
	for _, p := range pids {
		err := os.Remove(filepath.Join(dir, strconv.FormatUint(p, 10)+".json"))
		if err == nil {
			n++
		} else if !errors.Is(err, os.ErrNotExist) {
			return n, err
		}
	}
	return n, nil
}
