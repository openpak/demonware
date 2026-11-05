package ps3

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Everything permanent lives in Postgres: the issued lobby tickets (by SHA-256, never the
// ticket), each player's stored files (Modern Warfare 3: mpdata, the stats) and the games a
// host has open. With no pool (unit tests, DEMONWARE_ALLOW_FILE_STORE=1 runs) it keeps
// them in memory.

const schema = `
CREATE TABLE IF NOT EXISTS dw_ps3_sessions (
  hash       bytea PRIMARY KEY,
  pid        bigint NOT NULL,
  account_id text NOT NULL,
  online_id  text NOT NULL,
  title      bigint NOT NULL,
  issued     timestamptz NOT NULL,
  expires    timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS dw_ps3_sessions_account ON dw_ps3_sessions (account_id);
CREATE INDEX IF NOT EXISTS dw_ps3_sessions_expires ON dw_ps3_sessions (expires);
CREATE TABLE IF NOT EXISTS dw_ps3_files (
  title      bigint NOT NULL,
  pid        bigint NOT NULL,
  name       text NOT NULL,
  data       bytea NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (title, pid, name)
);
CREATE INDEX IF NOT EXISTS dw_ps3_files_pid ON dw_ps3_files (pid);
CREATE TABLE IF NOT EXISTS dw_ps3_games (
  id         bytea PRIMARY KEY,
  title      bigint NOT NULL,
  pid        bigint NOT NULL,
  info       bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS dw_ps3_games_pid ON dw_ps3_games (pid);`

// Session is one admitted player, found again by the lobby from the ticket they present.
type Session struct {
	PID       uint64
	AccountID string
	OnlineID  string
	Title     uint32
	Issued    time.Time
	Expires   time.Time
}

// Game is a game a host created on the matchmaking service.
type Game struct {
	ID      []byte
	Title   uint32
	PID     uint64
	Info    []byte // the create task's arguments, as sent
	Created time.Time
}

// Store is the PS3 Demonware store.
type Store struct {
	db *pgxpool.Pool

	mu       sync.Mutex
	sessions map[[32]byte]Session
	files    map[fkey][]byte
	games    map[string]Game
}

type fkey struct {
	title uint32
	pid   uint64
	name  string
}

// NewStore creates the tables (when db is set) and returns the store.
func NewStore(ctx context.Context, db *pgxpool.Pool) (*Store, error) {
	s := &Store{db: db, sessions: map[[32]byte]Session{}, files: map[fkey][]byte{}, games: map[string]Game{}}
	if db != nil {
		if _, err := db.Exec(ctx, schema); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func hashOf(ticket []byte) [32]byte { return sha256.Sum256(ticket) }

func (s *Store) PutSession(ctx context.Context, ticket []byte, v Session) error {
	h := hashOf(ticket)
	if s.db == nil {
		s.mu.Lock()
		s.sessions[h] = v
		s.mu.Unlock()
		return nil
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO dw_ps3_sessions (hash,pid,account_id,online_id,title,issued,expires)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, h[:], int64(v.PID), v.AccountID, v.OnlineID, int64(v.Title), v.Issued, v.Expires); err != nil {
		return err
	}
	_, _ = s.db.Exec(ctx, `DELETE FROM dw_ps3_sessions WHERE expires < now() - interval '1 hour'`)
	return nil
}

// Session finds the player a ticket was issued to; ok is false for an unknown or expired one.
func (s *Store) Session(ctx context.Context, ticket []byte) (Session, bool, error) {
	h := hashOf(ticket)
	var v Session
	if s.db == nil {
		s.mu.Lock()
		v, ok := s.sessions[h]
		s.mu.Unlock()
		return v, ok && time.Now().Before(v.Expires), nil
	}
	var pid, title int64
	err := s.db.QueryRow(ctx, `SELECT pid,account_id,online_id,title,issued,expires FROM dw_ps3_sessions
		WHERE hash=$1 AND expires > now()`, h[:]).Scan(&pid, &v.AccountID, &v.OnlineID, &title, &v.Issued, &v.Expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, false, nil
	}
	v.PID, v.Title = uint64(pid), uint32(title)
	return v, err == nil, err
}

func (s *Store) PutFile(ctx context.Context, title uint32, pid uint64, name string, data []byte) error {
	if s.db == nil {
		s.mu.Lock()
		s.files[fkey{title, pid, name}] = append([]byte(nil), data...)
		s.mu.Unlock()
		return nil
	}
	_, err := s.db.Exec(ctx, `INSERT INTO dw_ps3_files (title,pid,name,data) VALUES ($1,$2,$3,$4)
		ON CONFLICT (title,pid,name) DO UPDATE SET data=EXCLUDED.data, updated_at=now()`,
		int64(title), int64(pid), name, data)
	return err
}

func (s *Store) File(ctx context.Context, title uint32, pid uint64, name string) ([]byte, bool, error) {
	if s.db == nil {
		s.mu.Lock()
		d, ok := s.files[fkey{title, pid, name}]
		s.mu.Unlock()
		return d, ok, nil
	}
	var d []byte
	err := s.db.QueryRow(ctx, `SELECT data FROM dw_ps3_files WHERE title=$1 AND pid=$2 AND name=$3`,
		int64(title), int64(pid), name).Scan(&d)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return d, err == nil, err
}

func (s *Store) PutGame(ctx context.Context, g Game) error {
	if s.db == nil {
		s.mu.Lock()
		s.games[string(g.ID)] = g
		s.mu.Unlock()
		return nil
	}
	_, err := s.db.Exec(ctx, `INSERT INTO dw_ps3_games (id,title,pid,info) VALUES ($1,$2,$3,$4)
		ON CONFLICT (id) DO UPDATE SET info=EXCLUDED.info, updated_at=now()`,
		g.ID, int64(g.Title), int64(g.PID), g.Info)
	return err
}

// DeleteGames removes one game (id) or every game of a player (id nil); the number removed.
func (s *Store) DeleteGames(ctx context.Context, pid uint64, id []byte) (int, error) {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		n := 0
		for k, g := range s.games {
			if g.PID == pid && (id == nil || string(g.ID) == string(id)) {
				delete(s.games, k)
				n++
			}
		}
		return n, nil
	}
	if id == nil {
		tag, err := s.db.Exec(ctx, `DELETE FROM dw_ps3_games WHERE pid=$1`, int64(pid))
		return int(tag.RowsAffected()), err
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM dw_ps3_games WHERE pid=$1 AND id=$2`, int64(pid), id)
	return int(tag.RowsAffected()), err
}

// Games counts the open games (for /health).
func (s *Store) Games(ctx context.Context) int {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.games)
	}
	var n int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM dw_ps3_games`).Scan(&n)
	return n
}

// Erase deletes everything kept for an account (openpak/prds#120) and reports the rows removed.
func (s *Store) Erase(ctx context.Context, accountID string) (int, error) {
	pid := PID(accountID)
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		n := 0
		for h, v := range s.sessions {
			if v.AccountID == accountID {
				delete(s.sessions, h)
				n++
			}
		}
		for k := range s.files {
			if k.pid == pid {
				delete(s.files, k)
				n++
			}
		}
		for k, g := range s.games {
			if g.PID == pid {
				delete(s.games, k)
				n++
			}
		}
		return n, nil
	}
	rows := 0
	for _, q := range []struct {
		sql string
		arg any
	}{
		{`DELETE FROM dw_ps3_sessions WHERE account_id = $1`, accountID},
		{`DELETE FROM dw_ps3_files WHERE pid = $1`, int64(pid)},
		{`DELETE FROM dw_ps3_games WHERE pid = $1`, int64(pid)},
	} {
		tag, err := s.db.Exec(ctx, q.sql, q.arg)
		if err != nil {
			return rows, err
		}
		rows += int(tag.RowsAffected())
	}
	return rows, nil
}
