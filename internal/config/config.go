// Package config reads the same Config.json the closed-source OPQBot consumes, so an
// existing deployment can be pointed at this implementation without editing it.
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"openopq/internal/api"
	"os"
	"strings"
)

// FileListEntry is one downloadable plugin or script the framework keeps up to date.
type FileListEntry struct {
	CommitID string `json:"CommitID"`
	DownPath string `json:"DownPath"`
	FileDir  string `json:"FileDir"`
	FileName string `json:"FileName"`
	FileURL  string `json:"FileUrl"`
	RunType  int    `json:"RunType"`
}

// Config mirrors the published document. Version is the client identity the framework
// claims, and it has to agree with the build the signer speaks for.
type Config struct {
	CDNLists    []string        `json:"CDNLists"`
	FakeSignAPI string          `json:"FakeSignApi"`
	FileLists   []FileListEntry `json:"FileLists"`
	Version     string          `json:"Version"`
	Accounts    []string        `json:"Accounts,omitempty"`
	// Omitted means enabled. Explicit false stops retries after a QQ network outage.
	AutoReconnect    *bool `json:"AutoReconnect,omitempty"`
	PersistLogin     *bool `json:"PersistLogin,omitempty"`
	AutoRestoreLogin *bool `json:"AutoRestoreLogin,omitempty"`
	// Protocol selects opq (default), onebot11, onebot12, both (OPQ + v11),
	// or all (OPQ + v11 + v12).
	Protocol string           `json:"Protocol"`
	OneBot   api.OneBotConfig `json:"OneBot,omitempty"`
}

// Protocols reports whether the OPQ and/or OneBot v11 adapters are enabled.
func (c *Config) Protocols() (opq, onebot bool) {
	switch c.Protocol {
	case "onebot12", "ob12":
		return false, false
	case "onebot11", "onebot", "ob11":
		return false, true
	case "both", "all":
		return true, true
	default: // "opq" or empty
		return true, false
	}
}

// Load reads and validates a configuration file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var config Config
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	config.FakeSignAPI = strings.TrimSpace(config.FakeSignAPI)
	if config.FakeSignAPI == "" {
		return nil, fmt.Errorf("请先在配置文件填写 FakeSignApi：签名服务的完整 http(s)://主机:端口/sign 地址；客户端不包含签名器")
	}
	endpoint, err := url.Parse(config.FakeSignAPI)
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.RawQuery != "" || endpoint.Fragment != "" || !strings.HasSuffix(endpoint.Path, "/sign") {
		return nil, fmt.Errorf("FakeSignApi must be a complete HTTP(S) URL ending in /sign, without query or fragment")
	}
	if endpoint.User != nil && endpoint.Scheme != "https" && endpoint.Hostname() != "127.0.0.1" && endpoint.Hostname() != "localhost" && endpoint.Hostname() != "::1" {
		return nil, fmt.Errorf("signer credentials require HTTPS, except on loopback")
	}
	normalized, err := api.NormalizeOneBotConfig(config.OneBot)
	if err != nil {
		return nil, fmt.Errorf("OneBot config: %w", err)
	}
	config.OneBot = normalized
	return &config, nil
}

// SignEndpoint returns the signature service URL, or empty when the document does not
// name one.
func (c *Config) SignEndpoint() string { return c.FakeSignAPI }

func (c *Config) OneBot12Enabled() bool {
	return c.Protocol == "onebot12" || c.Protocol == "ob12" || c.Protocol == "all"
}
