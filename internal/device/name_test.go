package device

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func TestComputerNameIsStableAndPlatformSpecific(t *testing.T) {
	guid, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	mac := ComputerName(guid, PlatformMac)
	again := ComputerName(append([]byte(nil), guid...), PlatformMac)
	if mac != again {
		t.Fatalf("name moved: %q vs %q", mac, again)
	}
	if !strings.Contains(mac, "'s ") {
		t.Fatalf("mac name %q is not possessive", mac)
	}
	model := strings.TrimPrefix(mac, strings.SplitN(mac, "'s ", 2)[0]+"'s ")
	allowed := map[string]bool{}
	for _, m := range macModels {
		allowed[m] = true
	}
	if !allowed[model] {
		t.Fatalf("mac model %q is not in the wordlist", model)
	}
	linux := ComputerName(guid, PlatformLinux)
	if linux == mac {
		t.Fatalf("linux reused the mac name %q", mac)
	}
	if !strings.Contains(linux, "'s Ubuntu ") {
		t.Fatalf("linux name %q is not an Ubuntu display name", linux)
	}
}

func TestComputerNameOmitsTheExampleGivenName(t *testing.T) {
	for _, name := range englishGivenNames {
		if strings.EqualFold(name, "Shia") {
			t.Fatal("the example given name was added to the wordlist")
		}
	}
	for i := 0; i < 256; i++ {
		guid := bytes.Repeat([]byte{byte(i)}, 16)
		for _, platform := range []Platform{PlatformMac, PlatformLinux} {
			got := ComputerName(guid, platform)
			if strings.HasPrefix(strings.ToLower(got), "shia'") {
				t.Fatalf("generated %q", got)
			}
		}
	}
}

func TestDetectPlatformFollowsEnvThenVersion(t *testing.T) {
	t.Setenv("OPENOPQ_DEVICE_PLATFORM", "")
	if got := DetectPlatform("QQNT-v7.0.2-53644"); got != PlatformMac {
		t.Fatalf("default 7.0.2 platform: %q", got)
	}
	if got := DetectPlatform("QQNT-v3.2.0-linux"); got != PlatformLinux {
		t.Fatalf("linux version platform: %q", got)
	}
	t.Setenv("OPENOPQ_DEVICE_PLATFORM", "linux")
	if got := DetectPlatform("QQNT-v7.0.2-53644"); got != PlatformLinux {
		t.Fatalf("env linux did not override mac version: %q", got)
	}
	t.Setenv("OPENOPQ_DEVICE_PLATFORM", "mac")
	if got := DetectPlatform("linux-qq"); got != PlatformMac {
		t.Fatalf("env mac did not override linux version: %q", got)
	}
}
