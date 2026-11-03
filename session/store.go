package session

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/openpak/demonware/account"
)

// Postgres keeps the tickets permanently. The in-memory map is only a cache in front of it: Find
// falls back to the table, so a restart loses no ticket.

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
  id       text PRIMARY KEY,
  pid      bigint NOT NULL,
  title    bigint NOT NULL,
  ticket   bytea NOT NULL,
  player   text NOT NULL,
  issued   timestamptz NOT NULL,
  expires  timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_pid ON sessions (pid);
CREATE INDEX IF NOT EXISTS sessions_expires ON sessions (expires);`

// OpenDB opens the permanent store (a pool of 3 connections at most). Sessions found in the legacy
// JSON file at legacyFile (may be "") that the table does not have yet are imported once.
func OpenDB(ctx context.Context, url, legacyFile string, ttl time.Duration) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("DEMONWARE_DATABASE_URL: invalid")
	}
	cfg.MaxConns = 3
	var pool *pgxpool.Pool
	for i := 0; ; i++ { // Postgres may come up after this unit
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				break
			}
			pool.Close()
		}
		if i >= 30 {
			return nil, fmt.Errorf("session store unreachable: %w", err)
		}
		time.Sleep(2 * time.Second)
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("session store schema: %w", err)
	}
	s := &Store{TTL: ttl, now: time.Now, byID: map[string]*Session{}, db: pool}
	if legacyFile != "" {
		if raw, err := os.ReadFile(legacyFile); err == nil {
			var list []*Session
			if json.Unmarshal(raw, &list) == nil {
				n := 0
				for _, v := range list {
					if len(v.Ticket) == Size && s.now().Before(v.Expires) {
						if s.dbSave(v) == nil {
							n++
						}
					}
				}
				if n > 0 {
					log.Printf("session store: imported %d sessions from the legacy file", n)
					_ = os.Rename(legacyFile, legacyFile+".imported")
				}
			}
		}
	}
	return s, nil
}

// Pool is the shared Postgres pool, nil when tickets are in a file.
func (s *Store) Pool() *pgxpool.Pool { return s.db }

// Close releases the pool.
func (s *Store) Close() {
	if s.db != nil {
		s.db.Close()
	}
}

func (s *Store) dbSave(v *Session) error {
	pl, err := json.Marshal(v.Player)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.db.Exec(ctx, `INSERT INTO sessions (id, pid, title, ticket, player, issued, expires)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (id) DO NOTHING`,
		v.id(), int64(v.Player.PID), int64(v.Title), v.Ticket, string(pl), v.Issued, v.Expires); err != nil {
		return err
	}
	_, _ = s.db.Exec(ctx, `DELETE FROM sessions WHERE expires < now() - interval '1 hour'`)
	return nil
}

func (s *Store) dbLoad(id string) *Session {
	if _, err := hex.DecodeString(id); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var v Session
	var title int64
	var pl string
	err := s.db.QueryRow(ctx, `SELECT ticket, title, player, issued, expires FROM sessions WHERE id = $1`, id).
		Scan(&v.Ticket, &title, &pl, &v.Issued, &v.Expires)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("session store: lookup: %v", err)
		}
		return nil
	}
	v.Title = uint32(title)
	var p account.Player
	if json.Unmarshal([]byte(pl), &p) != nil {
		return nil
	}
	v.Player = p
	return &v
}

func (s *Store) dbDrop(pid uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE pid = $1`, int64(pid)); err != nil {
		log.Printf("session store: drop: %v", err)
	}
}
