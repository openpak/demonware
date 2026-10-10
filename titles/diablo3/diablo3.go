// Package diablo3 is Diablo III: Eternal Collection (Demonware title 5745, "crimson").
// Everything it needs beyond the shared services is its publisher files; the services it
// calls that no one has decoded yet (leaderboards, hero upload, mail, counters, event log)
// get the empty success the lobby gives any unknown task (docs/protocol.md §7).
package diablo3

import (
	"log/slog"

	"github.com/openpak/demonware"
	"github.com/openpak/demonware/lobby"
	"github.com/openpak/demonware/redact"
	"github.com/openpak/demonware/wire"
)

const (
	svcStorage        = 10
	taskPublisherFile = 21
)

// New returns the title. settings is the path of the publisher settings file ("" = defaults).
func New(settings string, log *slog.Logger) *demonware.Title {
	return &demonware.Title{
		Slug: "diablo-3", ID: 5745, Name: "Diablo III: Eternal Collection", AppID: "01001B300B9BE000",
		AuthHosts: []string{
			"crimson-switch-auth3.prod.demonware.net", "crimson-switch-auth3.cert.demonware.net",
			"crimson-switch-auth3.dev.demonware.net",
		},
		LobbyHosts: []string{
			"crimson-switch-lobby.prod.demonware.net", "crimson-switch-lobby.cert.demonware.net",
			"crimson-switch-lobby.dev.demonware.net",
		},
		Match: Match,
		Handle: func(c *lobby.Conn, r *lobby.Request) (lobby.Response, bool) {
			if r.Service != svcStorage || r.Task != taskPublisherFile {
				return lobby.Response{}, false
			}
			return lobby.Response{Body: publisherFile(c, r, settings, log)}, true
		},
	}
}

func publisherFile(c *lobby.Conn, r *lobby.Request, path string, log *slog.Logger) []byte {
	_, e1 := r.Args.String() // context
	name, e2 := r.Args.String()
	if e1 != nil || e2 != nil {
		return wire.Fail(wire.Failed)
	}
	s, err := Load(path)
	if err != nil {
		log.Error("diablo3: settings unreadable, serving defaults", "err", redact.Err(err))
	}
	data := s.File(name)
	// A name the catalog does not know is the client's own text; keep it out of the log.
	logged := redact.Tag(name)
	if data != nil {
		logged = name
	}
	c.Log().Info("publisher file", "name", logged, "bytes", len(data), "season", s.Season)
	if data == nil {
		return wire.Empty(r.Task) // a missing file is normal (update-1.cpk)
	}
	return wire.Rows(r.Task, func(w *wire.Writer) uint32 { w.Blob(data); return 1 })
}
