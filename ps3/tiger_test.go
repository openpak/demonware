package ps3

import (
	"encoding/hex"
	"testing"
)

func TestTigerVectors(t *testing.T) {
	bytes100 := make([]byte, 100)
	for i := range bytes100 {
		bytes100[i] = byte(i)
	}
	for _, c := range []struct {
		in   []byte
		want string
	}{
		{nil, "3293ac630c13f0245f92bbb1766e16167a4e58492dde73f3"},
		{[]byte("abc"), "2aab1484e8c158f2bfb8c5ff41b57a525129131c957b5f93"},
		{bytes100, "733222d3a033351fafd68c5ce8a4d833ba7420d44103cb6b"},
	} {
		d := Tiger(c.in)
		if got := hex.EncodeToString(d[:]); got != c.want {
			t.Errorf("Tiger(%d bytes) = %s, want %s", len(c.in), got, c.want)
		}
	}
	if got := hex.EncodeToString(IV(0x12345678)); got != "544fe1555cdfeaaf" {
		t.Errorf("IV = %s", got)
	}
}
