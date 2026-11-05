package ctr

import (
	"strings"
	"unicode/utf8"

	"github.com/openpak/demonware/lobby"
	"github.com/openpak/demonware/wire"
)

// Two services CTR calls on every login that carry no game state. Identified from the game's own
// binary (v983040 main): the id and task number are the immediate arguments of the client's
// startTask / startStructTask / initTaskBuffer calls inside the named methods.
const (
	svcMarketing   = 104 // bdMarketingComms: in-game promo messages
	svcObjectStore = 193 // bdObjectStore: Demonware publisher/user objects
)

// marketing answers bdMarketingComms (service 104).
//
//	1 getMessages(location)              typed args, rows
//	4 reportFullMessagesViewed           typed args, none
//	5 getMessages(location, locale, ...) typed args `str, str, u32, u32, u32`, rows of bdMarketingMessage
//	6 getMessages (struct request)       struct
//	7 reportMessagesViewed (struct)      struct
//
// OpenPak has no promotional campaigns, so the truthful answer to every getMessages is zero
// messages, and view reports are accepted and dropped (nothing is stored, nothing is sold).
func (t *Title) marketing(c *lobby.Conn, r *lobby.Request) lobby.Response {
	switch r.Task {
	case 6, 7:
		return resp(wire.StructReply(r.Task, nil))
	case 1, 4, 5:
		return resp(wire.Empty(r.Task))
	}
	c.Log().Info("ctr: marketing task not implemented", "task", r.Task)
	return resp(wire.Empty(r.Task))
}

// objectStore answers bdObjectStore (service 193). Every task is a struct task (the client uses
// startStructTask), so the reply is a struct. CTR only reads publisher objects at login (task 8
// getPublisherObject, task 16 getPublisherObjects); OpenPak publishes none, so those answer an
// empty struct, and user uploads are not accepted (nothing is stored). The object names asked
// for are logged so a published object can be added from evidence.
func (t *Title) objectStore(c *lobby.Conn, r *lobby.Request) lobby.Response {
	if r.Task == 8 || r.Task == 16 {
		c.Log().Info("ctr: publisher objects requested", "task", r.Task, "names", requestedNames(structArg(r)))
	}
	return resp(wire.StructReply(r.Task, nil))
}

// requestedNames lists the printable text fields of a request struct (client, context, object
// ids) to at most 8 entries of 64 bytes.
func requestedNames(b []byte) string {
	var out []string
	var walk func(b []byte, depth int)
	walk = func(b []byte, depth int) {
		fields, err := wire.ParsePB(b)
		if err != nil {
			return
		}
		for _, f := range fields {
			if len(out) >= 8 || f.Data == nil || len(f.Data) == 0 {
				continue
			}
			if printable(f.Data) {
				out = append(out, string(f.Data[:min(len(f.Data), 64)]))
			} else if depth < 3 {
				walk(f.Data, depth+1)
			}
		}
	}
	walk(b, 0)
	return strings.Join(out, ",")
}

func printable(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
