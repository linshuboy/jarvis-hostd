package state

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	PairingStateUnpaired = "unpaired"
	PairingStatePending  = "pending"
	PairingStatePaired   = "paired"
	PairingStateRevoked  = "revoked"
)

type State struct {
	RuntimeID       string `json:"runtime_id,omitempty"`
	RuntimeToken    string `json:"runtime_token,omitempty"`
	PairingState    string `json:"pairing_state,omitempty"`
	LastGatewayURL  string `json:"last_gateway_url,omitempty"`
	LastConnectedAt string `json:"last_connected_at,omitempty"`
	LastError       string `json:"last_error,omitempty"`
}

type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Load() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked()
}

func (s *Store) Save(state State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveUnlocked(normalizeState(state))
}

func (s *Store) Update(fn func(*State) error) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadUnlocked()
	if err != nil {
		return State{}, err
	}
	if err := fn(&current); err != nil {
		return State{}, err
	}
	current = normalizeState(current)
	if err := s.saveUnlocked(current); err != nil {
		return State{}, err
	}
	return current, nil
}

func EnsureRuntimeID(current *State) (bool, error) {
	if strings.TrimSpace(current.RuntimeID) != "" {
		return false, nil
	}
	value, err := newUUID()
	if err != nil {
		return false, err
	}
	current.RuntimeID = value
	return true, nil
}

func (s *Store) loadUnlocked() (State, error) {
	content, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return normalizeState(State{}), nil
		}
		return State{}, err
	}
	var current State
	if err := json.Unmarshal(content, &current); err != nil {
		return State{}, err
	}
	return normalizeState(current), nil
}

func (s *Store) saveUnlocked(current State) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(s.path, payload, 0o600)
}

func normalizeState(current State) State {
	current.RuntimeID = strings.TrimSpace(current.RuntimeID)
	current.RuntimeToken = strings.TrimSpace(current.RuntimeToken)
	current.PairingState = strings.TrimSpace(strings.ToLower(current.PairingState))
	switch current.PairingState {
	case PairingStatePending, PairingStatePaired, PairingStateRevoked:
	default:
		current.PairingState = PairingStateUnpaired
	}
	current.LastGatewayURL = strings.TrimSpace(current.LastGatewayURL)
	current.LastConnectedAt = strings.TrimSpace(current.LastConnectedAt)
	current.LastError = strings.TrimSpace(current.LastError)
	return current
}

func newUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		raw[0:4],
		raw[4:6],
		raw[6:8],
		raw[8:10],
		raw[10:16],
	), nil
}
