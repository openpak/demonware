// Package session issues Demonware tickets and remembers them, so the lobby can prove that a
// handshake comes from a player auth3 admitted. docs/protocol.md §2 is the ticket layout.
//
// Only tickets issued here are ever accepted: the lobby looks a ticket up by the random id it
// carries and compares every byte, so a client can neither invent a key nor fall back to an
// all-zero one. Sessions expire, and they are written to one JSON file so a restart does not
// throw every player offline.
package session

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/openpak/demonware/account"
)

// Magic is the ticket's first four bytes (DE AD BD EF): the client then reads it in the clear.
const Magic uint32 = 0xEFBDADDE

// Size of a ticket.
const Size = 128

// Title ids that change the ticket layout.
const TitleCTR = 5775

// Session is one admitted login.
type Session struct {
	Ticket  []byte         `json:"ticket"` // 128 bytes, what both tickets carry
	Title   uint32         `json:"title"`
	Player  account.Player `json:"player"`
	Issued  time.Time      `json:"issued"`
	Expires time.Time      `json:"expires"`
}

// LobbyKey is the 24-byte key that signs the lobby handshake.
func (s *Session) LobbyKey() []byte { return s.Ticket[97:121] }

func (s *Session) id() string { return hex.EncodeToString(s.Ticket[33:49]) }

// Store holds the live sessions.
type Store struct {
	TTL  time.Duration
	path string // "" keeps them in memory only
	now  func() time.Time

	mu   sync.Mutex
	byID map[string]*Session // a cache when db is set; the only copy otherwise (tests)
	db   *pgxpool.Pool       // the permanent store; nil keeps the JSON file / memory
}

// Open loads the sessions file at path (created on first write). An empty path keeps
// sessions in memory.
func Open(path string, ttl time.Duration) (*Store, error) {
	s := &Store{TTL: ttl, path: path, now: time.Now, byID: map[string]*Session{}}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Session
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	for _, v := range list {
		if len(v.Ticket) == Size && s.now().Before(v.Expires) {
			s.byID[v.id()] = v
		}
	}
	return s, nil
}

// Issue mints a ticket for p on title.
func (s *Store) Issue(p *account.Player, title uint32) (*Session, error) {
	now := s.now()
	t := make([]byte, Size)
	binary.LittleEndian.PutUint32(t[0:], Magic)
	t[4] = 1
	binary.LittleEndian.PutUint32(t[5:], title)
	binary.LittleEndian.PutUint32(t[13:], uint32(now.Unix()))
	binary.LittleEndian.PutUint64(t[17:], p.OnlineID())
	if title == TitleCTR {
		binary.LittleEndian.PutUint64(t[25:], p.OnlineID()) // CTR's online id
	} else {
		binary.LittleEndian.PutUint64(t[25:], uint64(now.Add(s.TTL).Unix()))
	}
	if _, err := rand.Read(t[33:97]); err != nil {
		return nil, err
	}
	copy(t[97:121], t[33:57])

	sess := &Session{Ticket: t, Title: title, Player: *p, Issued: now, Expires: now.Add(s.TTL)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[sess.id()] = sess
	if s.db != nil {
		return sess, s.dbSave(sess)
	}
	return sess, s.saveLocked()
}

// Find returns the live session whose ticket is exactly t.
func (s *Store) Find(t []byte) *Session {
	if len(t) != Size || binary.LittleEndian.Uint32(t) != Magic {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.byID[hex.EncodeToString(t[33:49])]
	if v == nil && s.db != nil {
		// Not in the cache (a restart, or another process issued it): the store has the truth.
		if v = s.dbLoad(hex.EncodeToString(t[33:49])); v != nil {
			s.byID[v.id()] = v
		}
	}
	if v == nil || !s.now().Before(v.Expires) || string(v.Ticket) != string(t) {
		return nil
	}
	return v
}

// Drop forgets every session of pid (a banned account).
func (s *Store) Drop(pid uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, v := range s.byID {
		if v.Player.PID == pid {
			delete(s.byID, id)
		}
	}
	if s.db != nil {
		s.dbDrop(pid)
		return
	}
	_ = s.saveLocked()
}

func (s *Store) saveLocked() error {
	now := s.now()
	list := make([]*Session, 0, len(s.byID))
	for id, v := range s.byID {
		if !now.Before(v.Expires) {
			delete(s.byID, id)
			continue
		}
		list = append(list, v)
	}
	if s.path == "" {
		return nil
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return WriteFileAtomic(s.path, raw)
}

// WriteFileAtomic replaces path with data through a synced temporary file.
func WriteFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// PIDsFor returns the PIDs of every session (live or stored) whose player has one of the given
// network service account ids.
func (s *Store) PIDsFor(ctx context.Context, subs []string) ([]uint64, error) {
	set := map[uint64]bool{}
	s.mu.Lock()
	for _, v := range s.byID {
		for _, sub := range subs {
			if v.Player.BaasUserID == sub {
				set[v.Player.PID] = true
			}
		}
	}
	s.mu.Unlock()
	if s.db != nil {
		rows, err := s.db.Query(ctx, `SELECT DISTINCT pid FROM sessions WHERE player::jsonb->>'baas_user_id' = ANY($1)`, subs)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var pid int64
			if err := rows.Scan(&pid); err != nil {
				return nil, err
			}
			set[uint64(pid)] = true
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	out := make([]uint64, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	return out, nil
}

// Erase deletes every session of the given PIDs or network service account ids and reports how
// many it removed (stored rows when there is a database, otherwise cached/file entries).
func (s *Store) Erase(ctx context.Context, subs []string, pids []uint64) (int, error) {
	isSub := map[string]bool{}
	for _, x := range subs {
		isSub[x] = true
	}
	isPID := map[uint64]bool{}
	ip := make([]int64, 0, len(pids))
	for _, p := range pids {
		isPID[p] = true
		ip = append(ip, int64(p))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mem := 0
	for id, v := range s.byID {
		if isSub[v.Player.BaasUserID] || isPID[v.Player.PID] {
			delete(s.byID, id)
			mem++
		}
	}
	if s.db != nil {
		tag, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE pid = ANY($1) OR player::jsonb->>'baas_user_id' = ANY($2)`, ip, subs)
		return int(tag.RowsAffected()), err
	}
	if mem > 0 {
		return mem, s.saveLocked()
	}
	return 0, nil
}
