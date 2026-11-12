// Package device persists a virtualized device identity for each QQ account.
//
// The identity that must stay stable across restarts is the 16-byte GUID plus a
// generated computer name: login tickets are bound to both, and QQ treats a new
// GUID as a new machine. OS/kernel strings still describe the signer build and
// are constants; the computer name is drawn once from the GUID and kept.
package device

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Profile struct {
	Account      string     `json:"account"`
	GUID         string     `json:"guid"`
	ComputerName string     `json:"computer_name,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLogin    *time.Time `json:"last_login,omitempty"`
}

type Store struct {
	mu       sync.Mutex
	dir      string
	platform Platform
}

func New(dir string) (*Store, error) {
	return NewWithPlatform(dir, PlatformMac)
}

func NewWithPlatform(dir string, platform Platform) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &Store{dir: dir, platform: NormalizePlatform(platform)}, nil
}

func (s *Store) Platform() Platform {
	if s == nil {
		return PlatformMac
	}
	return NormalizePlatform(s.platform)
}

func Account(text string) (string, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(text), 10, 32)
	if err != nil || n == 0 {
		return "", fmt.Errorf("QQ account must be a non-zero 32-bit number")
	}
	return strconv.FormatUint(n, 10), nil
}

func (s *Store) read(account string) (Profile, error) {
	var p Profile
	raw, err := os.ReadFile(filepath.Join(s.dir, account+".json"))
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("device profile %s is invalid: %w", account, err)
	}
	guid, err := hex.DecodeString(p.GUID)
	if err != nil || len(guid) != 16 || p.Account != account {
		return p, fmt.Errorf("device profile %s is invalid; refusing to replace its identity", account)
	}
	return p, nil
}

func (s *Store) save(p Profile) error {
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".device-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, filepath.Join(s.dir, p.Account+".json"))
}

func (s *Store) withName(p Profile) (Profile, bool) {
	if strings.TrimSpace(p.ComputerName) != "" {
		return p, false
	}
	raw, err := hex.DecodeString(p.GUID)
	if err != nil || len(raw) != 16 {
		return p, false
	}
	p.ComputerName = ComputerName(raw, s.platform)
	return p, true
}

func (s *Store) named(p Profile) (Profile, error) {
	p, dirty := s.withName(p)
	if dirty {
		if err := s.save(p); err != nil {
			return Profile{}, err
		}
	}
	return p, nil
}

func (s *Store) Peek(text string) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, err := Account(text)
	if err != nil {
		return Profile{}, err
	}
	p, err := s.read(account)
	if err != nil {
		return p, err
	}
	return s.named(p)
}

func (s *Store) GetOrCreate(text string) (Profile, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, err := Account(text)
	if err != nil {
		return Profile{}, false, err
	}
	p, err := s.read(account)
	if err == nil {
		p, err = s.named(p)
		return p, false, err
	}
	if !os.IsNotExist(err) {
		return Profile{}, false, err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return Profile{}, false, err
	}
	p = Profile{Account: account, GUID: hex.EncodeToString(raw), ComputerName: ComputerName(raw, s.platform), CreatedAt: time.Now().UTC()}
	if err := s.save(p); err != nil {
		return Profile{}, false, err
	}
	return p, true, nil
}

func (s *Store) MarkLogin(account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, err := Account(account)
	if err != nil {
		return err
	}
	p, err := s.read(account)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	p.LastLogin = &now
	return s.save(p)
}

// BindLogin saves a device learned from a completed anonymous login. Existing
// account identities are retained; the returned profile may use another GUID.
func (s *Store) BindLogin(text, guid string) (Profile, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, err := Account(text)
	if err != nil {
		return Profile{}, false, err
	}
	raw, err := hex.DecodeString(guid)
	if err != nil || len(raw) != 16 {
		return Profile{}, false, fmt.Errorf("device GUID must contain 16 bytes")
	}
	p, err := s.read(account)
	created := os.IsNotExist(err)
	if err != nil && !created {
		return Profile{}, false, err
	}
	now := time.Now().UTC()
	if created {
		p = Profile{Account: account, GUID: hex.EncodeToString(raw), ComputerName: ComputerName(raw, s.platform), CreatedAt: now}
	} else {
		p, _ = s.withName(p)
	}
	// A login on a new device does not imply that the saved device was used.
	if p.GUID == hex.EncodeToString(raw) {
		p.LastLogin = &now
		if err := s.save(p); err != nil {
			return Profile{}, false, err
		}
	}
	return p, created, nil
}

func (s *Store) List() ([]Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	profiles := make([]Profile, 0)
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		account := strings.TrimSuffix(file.Name(), ".json")
		if _, err := Account(account); err != nil {
			continue
		}
		p, err := s.read(account)
		if err != nil {
			return nil, err
		}
		p, err = s.named(p)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].CreatedAt.After(profiles[j].CreatedAt) })
	return profiles, nil
}
