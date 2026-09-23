// Package session issues Demonware tickets and remembers them, so the lobby can prove that a
// handshake comes from a player auth3 admitted. docs/protocol.md §2 is the ticket layout.
//
// Only tickets issued here are ever accepted: the lobby looks a ticket up by the random id it
// carries and compares every byte, so a client can neither invent a key nor fall back to an
// all-zero one. Sessions expire, and they are written to one JSON file so a restart does not
// throw every player offline.
package session

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

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
	byID map[string]*Session
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
