package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMediaCacheConfig(t *testing.T) {
	for _, tc := range []struct {
		value   string
		want    int64
		invalid bool
	}{
		{"", 0, false}, {`,"MediaCacheMiB":1024`, 1024, false},
		{`,"MediaCacheMiB":-1`, 0, true}, {`,"MediaCacheMiB":8796093022208`, 0, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Config.json")
			if err := os.WriteFile(path, []byte(`{"FakeSignApi":"http://localhost/sign"`+tc.value+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if (err != nil) != tc.invalid {
				t.Fatalf("config error = %v", err)
			}
			if err == nil && cfg.MediaCacheMiB != tc.want {
				t.Fatalf("value = %d", cfg.MediaCacheMiB)
			}
		})
	}
}
