// Package titles is the registry of the games this server can host, selected by
// DEMONWARE_TITLES. Adding a game is a package under titles/ and one line here.
package titles

import (
	"log/slog"
	"path/filepath"

	"github.com/openpak/demonware"
	"github.com/openpak/demonware/titles/ctr"
	"github.com/openpak/demonware/titles/diablo3"
)

// Env is what a title may need from the deployment.
type Env struct {
	DataDir    string
	D3Settings string // Diablo III publisher settings file
	CTRStart   int64  // Wumpa Coins a new CTR wallet opens with
	Log        *slog.Logger
}

// All builds each known title by slug.
var All = map[string]func(Env) (*demonware.Title, error){
	"diablo-3": func(e Env) (*demonware.Title, error) {
		return diablo3.New(e.D3Settings, e.Log.With("game", "diablo-3")), nil
	},
	"crash-team-racing": func(e Env) (*demonware.Title, error) {
		t, _, err := ctr.New(filepath.Join(e.DataDir, "ctr-wallets"), e.CTRStart, e.Log.With("game", "crash-team-racing"))
		return t, err
	},
}
