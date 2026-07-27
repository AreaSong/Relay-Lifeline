package capture

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
)

const legacyKeyID = "legacy"

type Keyring struct {
	ActiveID string
	Keys     map[string][]byte
}

type KeyStatus struct {
	ActiveID    string         `json:"activeId"`
	Configured  []string       `json:"configured"`
	RecordsByID map[string]int `json:"recordsById"`
	Unresolved  int            `json:"unresolved"`
}

type RewrapResult struct {
	ActiveID  string `json:"activeId"`
	Updated   int    `json:"updated"`
	Unchanged int    `json:"unchanged"`
}

func KeyringFromEnvironment() (Keyring, error) {
	return ParseKeyring(
		os.Getenv("RELAY_LIFELINE_CAPTURE_ACTIVE_KEY_ID"),
		os.Getenv("RELAY_LIFELINE_CAPTURE_KEY"),
		os.Getenv("RELAY_LIFELINE_CAPTURE_KEYRING"),
	)
}

func ParseKeyring(activeID, legacyValue, encodedRing string) (Keyring, error) {
	if encodedRing == "" {
		if activeID == "" {
			activeID = legacyKeyID
		}
		key, err := parseMasterKey(legacyValue)
		if err != nil {
			return Keyring{}, err
		}
		return Keyring{ActiveID: activeID, Keys: map[string][]byte{activeID: key}}, nil
	}
	var encoded map[string]string
	if err := json.Unmarshal([]byte(encodedRing), &encoded); err != nil {
		return Keyring{}, errors.New("RELAY_LIFELINE_CAPTURE_KEYRING must be a JSON object")
	}
	if activeID == "" {
		return Keyring{}, errors.New("RELAY_LIFELINE_CAPTURE_ACTIVE_KEY_ID is required with a key ring")
	}
	keys := make(map[string][]byte, len(encoded)+1)
	for id, value := range encoded {
		if id == "" {
			return Keyring{}, errors.New("capture key IDs cannot be empty")
		}
		key, err := parseMasterKey(value)
		if err != nil {
			return Keyring{}, fmt.Errorf("capture key %q is invalid", id)
		}
		keys[id] = key
	}
	if legacyValue != "" {
		key, err := parseMasterKey(legacyValue)
		if err != nil {
			return Keyring{}, err
		}
		if _, exists := keys[legacyKeyID]; !exists {
			keys[legacyKeyID] = key
		}
	}
	if _, exists := keys[activeID]; !exists {
		return Keyring{}, fmt.Errorf("active capture key %q is missing from the key ring", activeID)
	}
	return Keyring{ActiveID: activeID, Keys: keys}, nil
}

func (k Keyring) IDs() []string {
	ids := make([]string, 0, len(k.Keys))
	for id := range k.Keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
