package play

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// User data (service 29) lives in Postgres when Shared.DB is set, so a restart loses none of it.
// Without a database (local runs under DEMONWARE_ALLOW_FILE_STORE=1) it stays in memory.

const userDataSchema = `
CREATE TABLE IF NOT EXISTS dw_user_data (
  title      bigint NOT NULL,
  pid        bigint NOT NULL,
  ctx        text   NOT NULL,
  data       bytea  NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (title, pid, ctx)
);`

// OpenUserData creates the table; call it once at start before serving.
func OpenUserData(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, userDataSchema)
	return err
}

func (s *Shared) putUserData(k dataKey, data []byte) error {
	if s.DB == nil {
		s.mu.Lock()
		s.userData[k] = data
		s.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.DB.Exec(ctx, `INSERT INTO dw_user_data (title, pid, ctx, data) VALUES ($1,$2,$3,$4)
		ON CONFLICT (title, pid, ctx) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`,
		int64(k.title), int64(k.id), k.ctx, data)
	return err
}

func (s *Shared) getUserData(k dataKey) ([]byte, bool, error) {
	if s.DB == nil {
		s.mu.Lock()
		d, ok := s.userData[k]
		s.mu.Unlock()
		return d, ok, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var d []byte
	err := s.DB.QueryRow(ctx, `SELECT data FROM dw_user_data WHERE title=$1 AND pid=$2 AND ctx=$3`,
		int64(k.title), int64(k.id), k.ctx).Scan(&d)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return d, err == nil, err
}

// EraseUserData deletes the user data of pids (account erasure) and reports the rows removed.
func EraseUserData(ctx context.Context, db *pgxpool.Pool, pids []uint64) (int, error) {
	if db == nil || len(pids) == 0 {
		return 0, nil
	}
	ip := make([]int64, 0, len(pids))
	for _, p := range pids {
		ip = append(ip, int64(p))
	}
	tag, err := db.Exec(ctx, `DELETE FROM dw_user_data WHERE pid = ANY($1)`, ip)
	return int(tag.RowsAffected()), err
}
