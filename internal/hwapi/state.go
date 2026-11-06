package hwapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hansbogert/dsmr2homewizard/internal/dsmr"
)

// Latest holds the most recent valid telegram.
type Latest struct {
	mu     sync.RWMutex
	t      *dsmr.Telegram
	at     time.Time
	MaxAge time.Duration // 0 disables the staleness check
}

func (l *Latest) Set(t *dsmr.Telegram) {
	l.mu.Lock()
	l.t, l.at = t, time.Now()
	l.mu.Unlock()
}

// Get returns the latest telegram, or nil if there is none or it is older than MaxAge.
func (l *Latest) Get() *dsmr.Telegram {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.t == nil || (l.MaxAge > 0 && time.Since(l.at) > l.MaxAge) {
		return nil
	}
	return l.t
}

// persisted is what survives restarts: the identity clients pair against and their tokens.
type persisted struct {
	Serial       string            `json:"serial"`
	CloudEnabled bool              `json:"cloud_enabled"`
	APIv1Enabled bool              `json:"api_v1_enabled"`
	Tokens       map[string]string `json:"tokens"` // user name -> token
}

// State is the device's persisted settings, stored as JSON in the state directory.
type State struct {
	mu   sync.Mutex
	path string
	p    persisted
}

// LoadState reads dir/state.json, creating it with a fresh serial when absent. A non-empty
// serial overrides the stored one.
func LoadState(dir, serial string) (*State, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &State{path: filepath.Join(dir, "state.json"),
		p: persisted{APIv1Enabled: true, Tokens: map[string]string{}}}
	b, err := os.ReadFile(s.path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &s.p); err != nil {
			return nil, err
		}
		if s.p.Tokens == nil {
			s.p.Tokens = map[string]string{}
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return nil, err
	}
	if serial != "" {
		s.p.Serial = strings.ToLower(serial)
	}
	if s.p.Serial == "" {
		// 3c39e7 is the prefix of HomeWizard's own example serials.
		s.p.Serial = "3c39e7" + randomHex(3)
	}
	return s, s.saveLocked()
}

func (s *State) saveLocked() error {
	b, err := json.MarshalIndent(s.p, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *State) Serial() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.p.Serial
}

func (s *State) settings() (cloud, v1 bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.p.CloudEnabled, s.p.APIv1Enabled
}

func (s *State) update(cloud, v1 *bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cloud != nil {
		s.p.CloudEnabled = *cloud
	}
	if v1 != nil {
		s.p.APIv1Enabled = *v1
	}
	return s.saveLocked()
}

func (s *State) addUser(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok := strings.ToUpper(randomHex(16))
	s.p.Tokens[name] = tok
	return tok, s.saveLocked()
}

func (s *State) deleteUser(name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.p.Tokens[name]; !ok {
		return false, nil
	}
	delete(s.p.Tokens, name)
	return true, s.saveLocked()
}

// userFor returns the user name owning token, or "".
func (s *State) userFor(token string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, t := range s.p.Tokens {
		if t != "" && t == token {
			return name
		}
	}
	return ""
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
