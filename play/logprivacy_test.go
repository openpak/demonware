package play

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/openpak/demonware/wire"
)

// Game and search lines show attributes by type and value only: no ids, names or addresses.
func TestLogsCarryNoPersonalData(t *testing.T) {
	var out bytes.Buffer
	f := newFixture()
	f.lob.Log = slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f.shared.Log = f.lob.Log
	host := f.player(0x1122334455, "a1b2c3d4e5f60718", 5745, "203.0.113.41")
	join := f.player(0x5566778899, "0807060504030201", 5745, "198.51.100.62")
	addr := append([]byte{203, 0, 113, 41, 0x02, 0x0c}, make([]byte, 61)...)
	attrs := func(w *wire.Writer) { w.U32(1).U64(0x1122334455).String("Zorblaxa's game") }
	f.call(host, SvcMatchMaking, mmCreate, func(w *wire.Writer) { w.Blob(addr).U32(1).U32(4); attrs(w) })
	f.call(join, SvcMatchMaking, mmFind, func(w *wire.Writer) { w.U32(7).U32(0).U32(10); attrs(w) })
	got := out.String()
	for _, want := range []string{"game created", "games searched", "attrs=", "filters="} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in log:\n%s", want, got)
		}
	}
	for _, secret := range []string{"Zorblaxa", "203.0.113", "198.51.100", "73588229205", "1122334455", "a1b2c3d4"} {
		if strings.Contains(got, secret) {
			t.Errorf("log contains %q:\n%s", secret, got)
		}
	}
}
