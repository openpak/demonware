package ctr

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/openpak/demonware/session"
)

// Wumpa Coins; the game's currency table has 10, 11 and 12.
const coins = 10

var currencies = []uint64{10, 11, 12}

// wallet is one player's persistent Pit Stop state, one JSON file per OpenPak account.
type wallet struct {
	Balance    map[uint64]int64  `json:"balance"`
	Items      map[uint32]bool   `json:"items"`
	Races      map[string]int64  `json:"races,omitempty"`          // race receipt -> when it was paid
	Online     map[string]uint64 `json:"online_seconds,omitempty"` // UTC day -> online racing seconds
	Refresh    *refresh          `json:"pit_stop_refresh,omitempty"`
	Challenges map[string]uint64 `json:"challenges,omitempty"` // name@period -> completion time
}

type refresh struct {
	Day      string          `json:"day"`
	Count    uint64          `json:"count"`
	Receipts map[string]bool `json:"receipts"`
}

// Wallets stores wallets under a directory.
type Wallets struct {
	Dir   string
	Start int64 // Wumpa Coins a new wallet opens with

	mu sync.Mutex
}

var errFunds = errors.New("not enough Wumpa Coins")

func (s *Wallets) path(pid uint64) string {
	return filepath.Join(s.Dir, strconv.FormatUint(pid, 10)+".json")
}

func (s *Wallets) read(pid uint64) (*wallet, error) {
	if pid == 0 {
		return nil, errors.New("no account")
	}
	w := &wallet{}
	raw, err := os.ReadFile(s.path(pid))
	switch {
	case errors.Is(err, os.ErrNotExist):
		w.Balance = map[uint64]int64{coins: s.Start}
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(raw, w); err != nil {
			return nil, fmt.Errorf("wallet %d: %w", pid, err)
		}
	}
	if w.Balance == nil {
		w.Balance = map[uint64]int64{}
	}
	if w.Items == nil {
		w.Items = map[uint32]bool{}
	}
	return w, nil
}

func (s *Wallets) write(pid uint64, w *wallet) error {
	raw, err := json.Marshal(w)
	if err != nil {
		return err
	}
	return session.WriteFileAtomic(s.path(pid), raw)
}

// update loads, changes and saves one wallet under the lock. f's error aborts without saving.
func (s *Wallets) update(pid uint64, f func(w *wallet) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.read(pid)
	if err != nil {
		return err
	}
	if err := f(w); err != nil {
		return err
	}
	return s.write(pid, w)
}

// view reads a wallet without changing it.
func (s *Wallets) view(pid uint64) (*wallet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(pid)
}

func (w *wallet) owns(k sku) bool {
	if len(k.items) == 0 {
		return false
	}
	for _, it := range k.items {
		if !w.Items[it] {
			return false
		}
	}
	return true
}

func (w *wallet) price(k sku) int64 {
	if w.owns(k) {
		return 0
	}
	if len(k.parts) == 0 {
		return k.price
	}
	var total int64
	for _, p := range k.parts {
		if part, ok := catalog[p]; ok && !w.owns(part) {
			total += part.price
		}
	}
	return total
}

// Buy debits the SKUs and grants their items, all or nothing.
func (s *Wallets) Buy(pid uint64, skus []uint32) (int64, error) {
	var total int64
	err := s.update(pid, func(w *wallet) error {
		for _, id := range skus {
			k, ok := catalog[id]
			if !ok {
				return fmt.Errorf("unknown SKU %d", id)
			}
			p := w.price(k)
			if p > w.Balance[coins] {
				return errFunds
			}
			w.Balance[coins] -= p
			total += p
			for _, it := range k.items {
				w.Items[it] = true
			}
		}
		return nil
	})
	return total, err
}

// Inventory is the owned items, sorted.
func (w *wallet) inventory() []uint32 {
	out := make([]uint32, 0, len(w.Items))
	for it, ok := range w.Items {
		if ok {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// prune drops receipts and day counters older than any event still accepted.
func (w *wallet) prune(now time.Time) {
	cut := now.Add(-8 * 24 * time.Hour)
	for k, t := range w.Races {
		if time.Unix(t, 0).Before(cut) {
			delete(w.Races, k)
		}
	}
	for day := range w.Online {
		if d, err := time.Parse(time.DateOnly, day); err != nil || d.Before(cut) {
			delete(w.Online, day)
		}
	}
}
