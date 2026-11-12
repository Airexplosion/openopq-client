package device

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAccountIdentitySurvivesRestartAndDiffersBetweenAccounts(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	a, created, err := s.GetOrCreate("00123456")
	if err != nil || !created {
		t.Fatalf("first profile: %v %v", created, err)
	}
	b, _, err := s.GetOrCreate("234567")
	if err != nil {
		t.Fatal(err)
	}
	if a.GUID == b.GUID {
		t.Fatal("accounts share a device")
	}
	if err := s.MarkLogin(a.Account); err != nil {
		t.Fatal(err)
	}
	reopened, _ := New(dir)
	again, created, err := reopened.GetOrCreate("123456")
	if err != nil || created || again.GUID != a.GUID || again.LastLogin == nil {
		t.Fatalf("identity not reused: %+v %v %v", again, created, err)
	}
	list, err := reopened.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %v", list, err)
	}
}
func TestConcurrentLoginsKeepOneDevice(t *testing.T) {
	s, _ := New(t.TempDir())
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, _, err := s.GetOrCreate("123456")
			if err != nil {
				t.Error(err)
				return
			}
			ids <- p.GUID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("concurrent requests rotated device")
		}
	}
}
func TestCorruptProfileNeverSilentlyRotatesIdentity(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	file := filepath.Join(dir, "123456.json")
	_ = os.WriteFile(file, []byte(`{"account":"123456","guid":"broken"}`), 0600)
	if _, _, err := s.GetOrCreate("123456"); err == nil {
		t.Fatal("corrupt identity was replaced")
	}
	raw, _ := os.ReadFile(file)
	if string(raw) != `{"account":"123456","guid":"broken"}` {
		t.Fatal("profile overwritten")
	}
}
func TestAccountRejectsPathTraversalAndInvalidNumbers(t *testing.T) {
	s, _ := New(t.TempDir())
	for _, input := range []string{"../123", "0", "-1", "4294967296", "", "abc"} {
		if _, _, err := s.GetOrCreate(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestBindLoginPersistsNewIdentityAndPreservesExistingOne(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	const guid = "00112233445566778899aabbccddeeff"
	p, created, err := s.BindLogin("00123456", guid)
	raw, _ := hex.DecodeString(guid)
	wantName := ComputerName(raw, PlatformMac)
	if err != nil || !created || p.Account != "123456" || p.GUID != guid || p.LastLogin == nil || p.ComputerName != wantName {
		t.Fatalf("new anonymous login profile: %+v %v %v", p, created, err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "123456.json"))
	if err != nil {
		t.Fatal(err)
	}
	p, created, err = s.BindLogin("123456", "ffeeddccbbaa99887766554433221100")
	if err != nil || created || p.GUID != guid {
		t.Fatalf("existing anonymous login profile: %+v %v %v", p, created, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "123456.json"))
	if string(before) != string(after) {
		t.Fatal("a different device altered the saved profile")
	}
}

func TestBindLoginRejectsCorruptionAndInvalidGUID(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	for _, guid := range []string{"", "not-hex", "001122"} {
		if _, _, err := s.BindLogin("123456", guid); err == nil {
			t.Fatalf("accepted invalid GUID %q", guid)
		}
	}
	file := filepath.Join(dir, "123456.json")
	const invalid = `{"account":"123456","guid":"broken"}`
	if err := os.WriteFile(file, []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BindLogin("123456", "00112233445566778899aabbccddeeff"); err == nil {
		t.Fatal("corrupt saved profile replaced after anonymous login")
	}
	raw, _ := os.ReadFile(file)
	if string(raw) != invalid {
		t.Fatal("corrupt profile was overwritten")
	}
}

func TestComputerNameIsPersistedAndBackfilled(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	p, created, err := s.GetOrCreate("123456")
	if err != nil || !created || p.ComputerName == "" || !strings.Contains(p.ComputerName, "'s Mac") {
		t.Fatalf("new profile name: %+v %v %v", p, created, err)
	}
	reopened, _ := New(dir)
	again, created, err := reopened.GetOrCreate("123456")
	if err != nil || created || again.ComputerName != p.ComputerName {
		t.Fatalf("persisted name moved: %+v %v %v", again, created, err)
	}
	linuxDir := t.TempDir()
	linux, _ := NewWithPlatform(linuxDir, PlatformLinux)
	lp, _, err := linux.GetOrCreate("123456")
	if err != nil || !strings.Contains(lp.ComputerName, "'s Ubuntu ") {
		t.Fatalf("linux profile name: %+v %v", lp, err)
	}
	old := filepath.Join(t.TempDir(), "123456.json")
	storeDir := filepath.Dir(old)
	if err := os.WriteFile(old, []byte(`{"account":"123456","guid":"00112233445566778899aabbccddeeff","created_at":"2026-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	legacy, _ := New(storeDir)
	filled, created, err := legacy.GetOrCreate("123456")
	guid, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	if err != nil || created || filled.ComputerName != ComputerName(guid, PlatformMac) {
		t.Fatalf("backfill: %+v %v %v", filled, created, err)
	}
}
