package auth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/openpak/demonware/account"
)

// openPlayer names a player nx-baas did not prove, for DEMONWARE_REQUIRE_ACCOUNT=0 only: the
// id_token's own subject becomes the account id, so the same console keeps the same id.
func openPlayer(idToken, nickname string) *account.Player {
	sub := idToken
	if parts := strings.Split(idToken, "."); len(parts) == 3 {
		if raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "=")); err == nil {
			var c struct {
				Sub string `json:"sub"`
			}
			if json.Unmarshal(raw, &c) == nil && c.Sub != "" {
				sub = c.Sub
			}
		}
	}
	h := fnv.New64a()
	h.Write([]byte(sub))
	id := h.Sum64() | 1
	if nickname == "" {
		nickname = "Player"
	}
	return &account.Player{PID: id, BaasUserID: fmt.Sprintf("%016x", id), Nickname: nickname}
}
