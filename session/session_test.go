package session

import (
	"bytes"
	"encoding/binary"
	"path/filepath"
	"testing"
	"time"

	"github.com/openpak/demonware/account"
)

var player = &account.Player{PID: 1800000101, BaasUserID: "0123456789abcdef", Nickname: "Hansolo"}

func TestTicketLayout(t *testing.T) {
	s, _ := Open("", time.Hour)
	for _, title := range []uint32{5745, TitleCTR} {
		sess, err := s.Issue(player, title)
		if err != nil {
			t.Fatal(err)
		}
		tk := sess.Ticket
		le := binary.LittleEndian
		if len(tk) != Size || !bytes.Equal(tk[:4], []byte{0xDE, 0xAD, 0xBD, 0xEF}) || le.Uint32(tk[5:]) != title {
			t.Fatalf("header % x", tk[:9])
		}
		if le.Uint64(tk[17:]) != 0x0123456789abcdef {
			t.Fatal("user id is not the account's online id")
		}
		if title == TitleCTR && le.Uint64(tk[25:]) != 0x0123456789abcdef {
			t.Fatal("CTR online id missing at +25")
		}
		if title != TitleCTR && int64(le.Uint64(tk[25:])) <= time.Now().Unix() {
			t.Fatal("Diablo III expiry not in the future")
		}
		if !bytes.Equal(tk[97:121], tk[33:57]) || bytes.Equal(tk[97:121], make([]byte, 24)) {
			t.Fatal("lobby key")
		}
	}
}

func TestFindOnlyIssued(t *testing.T) {
	s, _ := Open("", time.Hour)
	sess, _ := s.Issue(player, 5745)
	if s.Find(sess.Ticket) != sess {
		t.Fatal("issued ticket not found")
	}
	forged := append([]byte(nil), sess.Ticket...)
	copy(forged[97:121], make([]byte, 24)) // same id, all-zero key
	if s.Find(forged) != nil {
		t.Fatal("a ticket with a changed key was accepted")
	}
	other := append([]byte(nil), sess.Ticket...)
	other[40] ^= 1
	if s.Find(other) != nil {
		t.Fatal("an unknown ticket was accepted")
	}
}

func TestExpiryAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	s, err := Open(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.now = func() time.Time { return now }
	sess, _ := s.Issue(player, 5745)

	again, err := Open(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got := again.Find(sess.Ticket)
	if got == nil || got.Player.PID != player.PID {
		t.Fatal("session lost across a restart")
	}
	again.now = func() time.Time { return now.Add(2 * time.Hour) }
	if again.Find(sess.Ticket) != nil {
		t.Fatal("expired session accepted")
	}
	again.now = time.Now
	again.Drop(player.PID)
	if again.Find(sess.Ticket) != nil {
		t.Fatal("dropped session accepted")
	}
}
