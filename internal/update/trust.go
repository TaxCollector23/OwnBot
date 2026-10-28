package update

import (
	"crypto/ed25519"
	_ "embed"
	"encoding/base64"
	"encoding/json"
)

// TrustedKeys contains only public release-verification keys, loaded from the
// shared trust file used by the npm installer. Private keys never belong here.
//
//go:embed trusted-keys.json
var trustedKeysJSON []byte

var TrustedKeys = loadTrustedKeys()

func loadTrustedKeys() map[string]ed25519.PublicKey {
	encoded := map[string]string{}
	if err := json.Unmarshal(trustedKeysJSON, &encoded); err != nil {
		panic("invalid embedded release trust keys: " + err.Error())
	}
	keys := make(map[string]ed25519.PublicKey, len(encoded))
	for id, value := range encoded {
		key, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(key) != ed25519.PublicKeySize {
			panic("invalid embedded Ed25519 key " + id)
		}
		keys[id] = ed25519.PublicKey(key)
	}
	return keys
}
