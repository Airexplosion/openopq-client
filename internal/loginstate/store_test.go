package loginstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openopq/internal/protocol"
)

func fixture() Record {
	return Record{Metadata: Metadata{Account: "123456", GUID: strings.Repeat("01", 16), Identity: strings.Repeat("a", 64)}, Credentials: protocol.Credentials{
		Token010A: []byte("secret-login-ticket"), Token0143: []byte("secret-d2"), SessionKey: bytes.Repeat([]byte{3}, 16), Uid: "secret-test-uid", NickName: "test nickname",
	}}
}

func TestEncryptedRestartPauseInvalidateAndDelete(t *testing.T) {
	dir := t.TempDir()
	store, _ := New(dir)
	record := fixture()
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	file, _ := name(record.Account, record.GUID)
	raw, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{record.Credentials.Token010A, []byte(record.Credentials.Uid), []byte(`"credentials"`)} {
		if bytes.Contains(raw, secret) {
			t.Fatal("credentials stored in plaintext")
		}
	}
	restarted, _ := New(dir)
	loaded, err := restarted.Load(record.Account, record.GUID, record.Identity)
	if err != nil || !loaded.AutoResume || !bytes.Equal(loaded.Credentials.SessionKey, record.Credentials.SessionKey) || loaded.SavedAt.IsZero() {
		t.Fatalf("restore failed: %v", err)
	}
	if err := restarted.Pause(record.Account, record.GUID); err != nil {
		t.Fatal(err)
	}
	loaded, err = restarted.Load(record.Account, record.GUID, record.Identity)
	if err != nil || loaded.AutoResume {
		t.Fatal("pause destroyed tickets or left auto-resume enabled")
	}
	if err := restarted.Save(loaded); err != nil {
		t.Fatal(err)
	}
	newRaw, _ := os.ReadFile(filepath.Join(dir, file))
	if bytes.Equal(raw, newRaw) {
		t.Fatal("nonce reused")
	}
	if err := restarted.Invalidate(record.Account, record.GUID, "QQ rejected authentication"); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Load(record.Account, record.GUID, record.Identity); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid login accepted: %v", err)
	}
	list, err := restarted.List()
	if err != nil || len(list) != 1 || list[0].AutoResume || list[0].InvalidReason == "" {
		t.Fatal("invalid login scheduled")
	}
	metadata, _ := json.Marshal(list)
	if bytes.Contains(metadata, record.Credentials.Token010A) || bytes.Contains(metadata, []byte("credentials")) {
		t.Fatal("metadata exposed tickets")
	}
	if err := restarted.Delete(record.Account, record.GUID); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Load(record.Account, record.GUID, record.Identity); !errors.Is(err, ErrMissing) {
		t.Fatal("delete did not remove login")
	}
}

func TestIdentityTamperingAndMissingKeyFailClosed(t *testing.T) {
	for _, mode := range []string{"protocol", "tamper", "renamed", "missing key", "wrong key"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			s, _ := New(dir)
			r := fixture()
			if err := s.Save(r); err != nil {
				t.Fatal(err)
			}
			file, _ := name(r.Account, r.GUID)
			path := filepath.Join(dir, file)
			switch mode {
			case "protocol":
				r.Identity = strings.Repeat("b", 64)
			case "tamper":
				data, _ := os.ReadFile(path)
				data[len(data)-1] ^= 1
				_ = os.WriteFile(path, data, 0600)
			case "renamed":
				data, _ := os.ReadFile(path)
				r.Account = "234567"
				file, _ = name(r.Account, r.GUID)
				_ = os.WriteFile(filepath.Join(dir, file), data, 0600)
			case "missing key":
				_ = os.Remove(filepath.Join(dir, "state.key"))
			case "wrong key":
				_ = os.WriteFile(filepath.Join(dir, "state.key"), make([]byte, 32), 0600)
			}
			if _, err := s.Load(r.Account, r.GUID, r.Identity); err == nil {
				t.Fatal("foreign/corrupt cache accepted")
			}
			if mode == "missing key" {
				if _, err := os.Stat(filepath.Join(dir, "state.key")); !os.IsNotExist(err) {
					t.Fatal("missing key silently replaced")
				}
			}
		})
	}
}

func TestListIsolatesDamagedAccountsAndBoundsData(t *testing.T) {
	s, _ := New(t.TempDir())
	r := fixture()
	if err := s.Save(r); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(s.dir, "234567-"+r.GUID+".enc"), []byte("damaged"), 0600)
	list, err := s.List()
	if err == nil || len(list) != 1 || list[0].Account != r.Account {
		t.Fatal("one damaged login blocked healthy accounts")
	}
	r.Credentials.Token010A = make([]byte, maxSize)
	if err := s.Save(r); err == nil {
		t.Fatal("oversized credentials accepted")
	}
	r = fixture()
	if _, err := s.Load(r.Account, r.GUID, r.Identity); err != nil {
		t.Fatal("failed write damaged previous checkpoint")
	}
	if _, err := s.Load("../escape", r.GUID, r.Identity); err == nil {
		t.Fatal("unsafe account path accepted")
	}
}
