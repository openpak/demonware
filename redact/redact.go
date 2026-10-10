// Package redact keeps personal data out of logs. A log line never carries a
// pid, account id, name, token, ticket or address; where two lines have to be
// matched up it carries a Tag instead.
package redact

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// key is random per process: tags correlate lines within one run and cannot be
// reversed or compared across restarts.
var key = func() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return k
}()

// Tag returns a short stable per-process tag for v ("~" and six hex digits).
func Tag(v any) string {
	m := hmac.New(sha256.New, key)
	fmt.Fprint(m, v)
	return "~" + hex.EncodeToString(m.Sum(nil)[:3])
}

// Err returns err's text without the addresses and URLs that net and url
// errors spell out ("read tcp 10.0.0.1:443->203.0.113.7:5000: ...").
func Err(err error) string {
	if err == nil {
		return ""
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return Err(ue.Err)
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return oe.Op + ": " + oe.Err.Error()
	}
	return err.Error()
}

// Path keeps the shape of a request path or route and tags every segment that
// is not a plain lowercase route word, so an id or a name in the path cannot
// reach a log line ("/sessions/ab12-9f/kick" -> "/sessions/~3fa9c1/kick").
func Path(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if s != "" && !routeWord(s) {
			segs[i] = Tag(s)
		}
	}
	return strings.Join(segs, "/")
}

func routeWord(s string) bool {
	if len(s) > 24 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

var addrRe = regexp.MustCompile(`\[[0-9a-fA-F:.%a-z]+\](:\d+)?|\b\d{1,3}(\.\d{1,3}){3}(:\d+)?`)

// Scrub replaces every IP address (with its port) in s with "~addr".
func Scrub(s string) string { return addrRe.ReplaceAllString(s, "~addr") }

type scrubWriter struct{}

func (scrubWriter) Write(p []byte) (int, error) {
	_, err := io.WriteString(log.Writer(), Scrub(string(p)))

	return len(p), err
}

// ErrorLog is for http.Server.ErrorLog and httputil.ReverseProxy.ErrorLog, whose
// own messages ("http: panic serving 203.0.113.7:5000", "TLS handshake error
// from ...") name the client. It writes to the standard logger, addresses scrubbed.
func ErrorLog() *log.Logger { return log.New(scrubWriter{}, "", 0) }
