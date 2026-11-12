package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignerMustBeExplicitAndValid(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		valid    bool
	}{
		{"", false}, {"  ", false}, {"localhost:18094", false},
		{"ftp://example.com/sign", false}, {"https://example.com", false},
		{"https://example.com/sign?key=private-test-key", false},
		{"http://user:private-test-key@example.com/sign", false},
		{"http://127.0.0.1:18094/sign", true},
		{"https://user:private-test-key@example.com/service/sign", true},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "Config.json")
			b, _ := json.Marshal(map[string]string{"FakeSignApi": tc.endpoint, "Version": "QQNT-v7.0.2-53644"})
			if err := os.WriteFile(p, b, 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(p)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-test-key") {
				t.Fatal("credential in validation error")
			}
			if cfg != nil && cfg.SignEndpoint() != tc.endpoint {
				t.Fatal("endpoint changed")
			}
		})
	}
}
