// Package loginstate stores QQ tickets encrypted, independently of SDK checkpoints.
package loginstate

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"openopq/internal/device"
	"openopq/internal/protocol"
)

var ErrMissing = errors.New("no saved QQ login")
var ErrInvalid = errors.New("saved QQ login requires a new scan")
var ErrIdentity = errors.New("saved QQ login does not match this device or protocol identity")

const maxSize = 1024 * 1024

type Metadata struct {
	Account       string    `json:"account"`
	GUID          string    `json:"guid"`
	Identity      string    `json:"-"`
	SavedAt       time.Time `json:"saved_at"`
	AutoResume    bool      `json:"auto_resume"`
	InvalidReason string    `json:"invalid_reason,omitempty"`
}

type Record struct {
	Metadata
	Credentials protocol.Credentials `json:"credentials"`
}

type Store struct {
	mu  sync.Mutex
	dir string
}

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func name(account, guid string) (string, error) {
	a, err := device.Account(account)
	raw, e := hex.DecodeString(guid)
	if err != nil || a != account || e != nil || len(raw) != 16 || hex.EncodeToString(raw) != guid {
		return "", ErrIdentity
	}
	return a + "-" + guid + ".enc", nil
}

func (s *Store) key(create bool) ([]byte, error) {
	path := filepath.Join(s.dir, "state.key")
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != 32 {
			return nil, errors.New("invalid QQ login encryption key")
		}
		return data, nil
	}
	if !os.IsNotExist(err) || !create {
		return nil, errors.New("QQ login encryption key unavailable")
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return s.key(false)
	}
	if err != nil {
		return nil, err
	}
	_, err = f.Write(key)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return key, nil
}

func (s *Store) aead(create bool) (cipher.AEAD, error) {
	key, err := s.key(create)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Identity is inside the ciphertext too; the filename is authenticated as AAD.
type envelope struct {
	Record
	Format   int    `json:"format"`
	Identity string `json:"identity"`
}

func (s *Store) read(file string) (Record, error) {
	f, err := os.Open(filepath.Join(s.dir, file))
	if os.IsNotExist(err) {
		return Record{}, ErrMissing
	}
	if err != nil {
		return Record{}, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	f.Close()
	if err != nil {
		return Record{}, err
	}
	if len(data) > maxSize {
		return Record{}, errors.New("QQ login checkpoint too large")
	}
	gcm, err := s.aead(false)
	if err != nil {
		return Record{}, err
	}
	if len(data) < gcm.NonceSize()+gcm.Overhead() {
		return Record{}, errors.New("invalid QQ login checkpoint")
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], []byte("openopq-qq-login-v1:"+file))
	if err != nil {
		return Record{}, errors.New("QQ login checkpoint authentication failed")
	}
	var e envelope
	if json.Unmarshal(plain, &e) != nil || e.Format != 1 {
		return Record{}, errors.New("invalid QQ login checkpoint format")
	}
	e.Record.Identity = e.Identity
	expected, err := name(e.Account, e.GUID)
	if err != nil || expected != file || len(e.Identity) != 64 || !e.Credentials.Complete() {
		return Record{}, ErrIdentity
	}
	return e.Record, nil
}

func (s *Store) write(record Record) error {
	file, err := name(record.Account, record.GUID)
	if err != nil {
		return err
	}
	if len(record.Identity) != 64 || !record.Credentials.Complete() {
		return ErrIdentity
	}
	plain, err := json.Marshal(envelope{Record: record, Format: 1, Identity: record.Identity})
	if err != nil {
		return err
	}
	if len(plain) > maxSize-64 {
		return errors.New("QQ login checkpoint too large")
	}
	gcm, err := s.aead(true)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	data := gcm.Seal(nonce, nonce, plain, []byte("openopq-qq-login-v1:"+file))
	f, err := os.CreateTemp(s.dir, ".login-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
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
	return os.Rename(tmp, filepath.Join(s.dir, file))
}

func (s *Store) Save(record Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record.SavedAt = time.Now().UTC()
	record.AutoResume = true
	record.InvalidReason = ""
	return s.write(record)
}

func (s *Store) Load(account, guid, identity string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := name(account, guid)
	if err != nil {
		return Record{}, err
	}
	record, err := s.read(file)
	if err != nil {
		return Record{}, err
	}
	if record.Identity != identity {
		return Record{}, ErrIdentity
	}
	if record.InvalidReason != "" {
		return Record{}, ErrInvalid
	}
	return record, nil
}

func (s *Store) Pause(account, guid string) error { return s.change(account, guid, "") }
func (s *Store) Invalidate(account, guid, reason string) error {
	return s.change(account, guid, reason)
}
func (s *Store) change(account, guid, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := name(account, guid)
	if err != nil {
		return err
	}
	r, err := s.read(file)
	if errors.Is(err, ErrMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	r.AutoResume = false
	if reason != "" {
		r.InvalidReason = reason
	}
	return s.write(r)
}

func (s *Store) Delete(account, guid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := name(account, guid)
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(s.dir, file))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// One damaged account must not prevent other saved accounts from being listed.
func (s *Store) List() ([]Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := []Metadata{}
	var failures []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".enc") {
			continue
		}
		r, err := s.read(entry.Name())
		if err != nil {
			failures = append(failures, fmt.Errorf("saved QQ login %s cannot be read", entry.Name()))
			continue
		}
		out = append(out, r.Metadata)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Account < out[j].Account })
	return out, errors.Join(failures...)
}
