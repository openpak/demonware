package play

import (
	"bytes"
	"strings"
	"testing"

	"github.com/openpak/demonware/lobby"
	"github.com/openpak/demonware/wire"
)

func (f *fixture) handle(c *lobby.Conn, svc, task byte, args func(w *wire.Writer)) lobby.Response {
	w := &wire.Writer{}
	w.B = []byte{svc}
	w.U8(task)
	args(w)
	w.B = append(w.B, 0)
	r := wire.NewReader(w.B)
	r.Off = 1
	_, _ = r.U8()
	resp, ok := f.shared.Handle(c, &lobby.Request{Service: svc, Task: task, Args: r, Raw: w.B})
	if !ok {
		return lobby.Response{}
	}
	return resp
}

func TestSendMessageRelaysToOnlineFriend(t *testing.T) {
	f := newFixture()
	a := f.player(1, "00000000000000a1", 5745, "10.0.0.1")
	b := f.player(2, "00000000000000b2", 5745, "10.0.0.2")
	other := f.player(3, "00000000000000c3", 5775, "10.0.0.3") // another title
	msg := []byte{1, 0, 0, 0, 9, 9, 9}

	resp := f.handle(a, SvcMessaging, taskSendMessage, func(w *wire.Writer) { w.U64(b.OnlineID()).Blob(msg) })
	if code, n := head(t, wire.NewReader(resp.Body)); code != 0 || n != 0 {
		t.Fatalf("reply code=%d n=%d, want an empty success", code, n)
	}
	if resp.Then == nil {
		t.Fatal("no push scheduled for an online recipient")
	}

	for _, to := range []uint64{other.OnlineID(), 0xdead, a.OnlineID()} {
		resp = f.handle(a, SvcMessaging, taskSendMessage, func(w *wire.Writer) { w.U64(to).Blob(msg) })
		if resp.Then != nil {
			t.Fatalf("push scheduled for %x (other title, offline or self)", to)
		}
		if code, _ := head(t, wire.NewReader(resp.Body)); code != 0 {
			t.Fatalf("reply code %d for an absent recipient, want success", code)
		}
	}

	resp = f.handle(a, SvcMessaging, taskSendMessage, func(w *wire.Writer) { w.U64(b.OnlineID()).Blob(make([]byte, maxMessage+1)) })
	if code, _ := head(t, wire.NewReader(resp.Body)); code == 0 {
		t.Fatal("oversized message accepted")
	}
}

func TestMessagePushLayout(t *testing.T) {
	data := []byte("party")
	r := wire.NewReader(messagePush(0x1122334455667788, "Leia", data))
	ev, _ := r.U32()
	from, _ := r.U64()
	name, _ := r.String()
	blob, err := r.Blob()
	if err != nil || ev != pushGlobalMessage || from != 0x1122334455667788 || name != "Leia" || !bytes.Equal(blob, data) {
		t.Fatalf("push = %d %x %q %q %v", ev, from, name, blob, err)
	}
}

func TestSenderNameFitsClientField(t *testing.T) {
	long := strings.Repeat("é", 40) // 80 bytes
	got := senderName(long)
	if len(got) > maxSenderName || !strings.HasPrefix(long, got) || len(got)%2 != 0 {
		t.Fatalf("senderName cut badly: %d bytes", len(got))
	}
}
