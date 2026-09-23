// Package demonware is OpenPak's Demonware server: auth3, the encrypted lobby and the remote
// tasks Activision's Demonware titles call on Nintendo Switch. One process serves every
// title, because the lobby (TCP 3074) and NAT (UDP 3074) ports are fixed by the games and
// shared by all of them; the title a connection belongs to is carried in its ticket.
package demonware

import "github.com/openpak/demonware/lobby"

// Title is one game the server hosts.
type Title struct {
	Slug  string // the DEMONWARE_TITLES value
	ID    uint32 // Demonware title id, as the client announces it
	Name  string
	AppID string // Switch title id

	AuthHosts, LobbyHosts []string // what DNS must answer for this title

	// Handle answers the title's own tasks; ok false passes the task to the shared services.
	Handle func(c *lobby.Conn, r *lobby.Request) (resp lobby.Response, ok bool)
	// Closed cleans up after a connection of this title.
	Closed func(c *lobby.Conn)
}

// NATHosts are the probe hosts every title uses.
var NATHosts = []string{
	"stun.us.demonware.net", "stun.eu.demonware.net", "stun.jp.demonware.net", "stun.au.demonware.net",
}
