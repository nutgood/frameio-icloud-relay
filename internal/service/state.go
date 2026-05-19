package service

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
)

// State is the persisted, mutated-by-service state file. Holds the
// webhook registration (so we don't re-register on every restart) and
// the set of Frame.io file IDs we've already imported into Photos.app
// but haven't yet successfully deleted from Frame.io. Persisting the
// latter is what makes the import → delete handoff crash-safe.
type State struct {
	WebhookID        string `json:"webhook_id,omitempty"`
	WebhookSecret    string `json:"webhook_secret,omitempty"`
	WebhookWorkspace string `json:"webhook_workspace,omitempty"`
	WebhookURL       string `json:"webhook_url,omitempty"`

	// Imported is the set of Frame.io file IDs that Photos.app has but
	// Frame.io still has too. Cleared per-ID once the Frame.io delete
	// confirms.
	Imported map[string]bool `json:"imported,omitempty"`

	mu sync.Mutex
}

// LoadState reads a state file, returning a zero-value State if missing.
func LoadState(path string) (*State, error) {
	s := &State{Imported: map[string]bool{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Imported == nil {
		s.Imported = map[string]bool{}
	}
	return s, nil
}

// SaveState atomically writes state to disk.
func SaveState(path string, s *State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// MarkImported / ForgetImported / HasImported manipulate the imported set.
func (s *State) MarkImported(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Imported == nil {
		s.Imported = map[string]bool{}
	}
	s.Imported[id] = true
}

func (s *State) ForgetImported(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Imported, id)
}

func (s *State) HasImported(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Imported[id]
}
