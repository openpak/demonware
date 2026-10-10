package lobby

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openpak/demonware/account"
	"github.com/openpak/demonware/session"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.String()
}

// A lobby connection must leave no pid, nickname, address or task bytes in the log, at debug
// level too; DEMONWARE_TRACE_BODIES=1 is the one switch that brings the task bytes back.
func TestLogsCarryNoPersonalData(t *testing.T) {
	for _, trace := range []bool{false, true} {
		t.Run(fmt.Sprintf("trace=%v", trace), func(t *testing.T) {
			old := TraceBodies
			TraceBodies = trace
			defer func() { TraceBodies = old }()

			const (
				pid  = 7788991122
				nick = "Zorblaxa"
				text = "SecretFreeText"
			)
			var out syncBuf
			store, _ := session.Open("", time.Hour)
			srv := &Server{Sessions: store, Log: slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})),
				Handle: func(c *Conn, r *Request) Response { return Response{} }}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go srv.Serve(ln)

			sess, _ := store.Issue(&account.Player{PID: pid, BaasUserID: "00000000000000aa", Nickname: nick}, 5745)
			cl := dial(t, ln.Addr().String())
			if reply := cl.handshake(sess.Ticket, sess.LobbyKey()); cl.ch == nil {
				t.Fatalf("handshake refused: % x", reply)
			}
			_, port, _ := net.SplitHostPort(cl.c.LocalAddr().String())

			// A peer that resets mid-hello: the net error spells out both addresses.
			rst := dial(t, ln.Addr().String())
			_, port2, _ := net.SplitHostPort(rst.c.LocalAddr().String())
			rst.c.Write([]byte("xx"))
			rst.c.(*net.TCPConn).SetLinger(0)
			rst.c.Close()
			deadline := time.Now().Add(3 * time.Second)
			for !strings.Contains(out.String(), "handshake refused") && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}

			for _, req := range [][]byte{
				append([]byte{12, 0x01, 6}, []byte(text)...), // a task with free text in its arguments
				{12, 0x01}, // too short to be a task
			} {
				f, _ := cl.ch.seal(innerTask, req)
				if _, err := cl.c.Write(f); err != nil {
					t.Fatal(err)
				}
				cl.readFrame()
			}
			cl.c.Close()
			deadline = time.Now().Add(3 * time.Second)
			for !strings.Contains(out.String(), "lobby closed") && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}

			got := out.String()
			if !strings.Contains(got, "pid=~") || !strings.Contains(got, "lobby connected") {
				t.Fatalf("connection lines missing or untagged:\n%s", got)
			}
			secrets := []string{"7788991122", nick, "127.0.0.1", port, port2}
			textHex := fmt.Sprintf("%x", text)
			if trace {
				if !strings.Contains(got, textHex) {
					t.Errorf("DEMONWARE_TRACE_BODIES did not turn the task dump on:\n%s", got)
				}
			} else {
				secrets = append(secrets, textHex)
			}
			for _, secret := range secrets {
				if strings.Contains(got, secret) {
					t.Errorf("log contains %q:\n%s", secret, got)
				}
			}
		})
	}
}
