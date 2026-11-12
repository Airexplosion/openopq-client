package device

import (
	"encoding/binary"
	"os"
	"strings"
)

// Platform is the signer-facing OS family used to pick a computer-name wordlist.
// It follows the QQ build being signed for, not the machine OpenOPQ runs on.
type Platform string

const (
	PlatformMac   Platform = "mac"
	PlatformLinux Platform = "linux"
)

// A Ubuntu Desktop install does not invent Apple-style possessives. The installer
// sets the hostname to "{username}-{dmi-product}": lowercase, spaces to hyphens,
// Lenovo/IBM usually taking DMI product-version (the ThinkPad string) instead of
// product-name. That is why a machine becomes alice-thinkpad-t14 rather than
// "Alice's Ubuntu Desktop". GNOME's "Device name" edits that hostname. QQ's
// device list is a display name, so Linux here uses the same possessive pattern
// as Mac rather than the installer hostname.
var (
	englishGivenNames = []string{
		"Ava", "Liam", "Olivia", "Noah", "Emma", "Oliver", "Sophia", "Elijah",
		"Isabella", "James", "Mia", "William", "Amelia", "Benjamin", "Harper", "Lucas",
		"Evelyn", "Henry", "Luna", "Alexander", "Ella", "Michael", "Scarlett", "Daniel",
		"Avery", "Jack", "Chloe", "Samuel", "Layla", "David", "Aria", "Joseph",
		"Penelope", "Owen", "Riley", "Sebastian", "Nora", "Jackson", "Hazel", "Aiden",
		"Aurora", "Matthew", "Violet", "Leo", "Stella", "John", "Grace", "Isaac",
		"Natalie", "Caleb", "Zoe", "Nathan", "Lily", "Ryan", "Hannah", "Asher",
		"Eleanor", "Isaiah", "Claire", "Thomas", "Paisley", "Levi", "Skylar", "Dylan",
		"Naomi", "Luke", "Savannah", "Gabriel", "Audrey", "Julian", "Brooklyn", "Anthony",
		"Alice", "Lincoln", "Eva", "Christopher", "Willow", "Joshua", "Quinn", "Andrew",
		"Ruby", "Theodore", "Ivy", "Mason", "Elena", "Ezra", "Clara", "Hudson",
		"Maya", "Connor", "Ada", "Eli", "Iris", "Adrian", "Jade", "Nolan",
		"Freya", "Parker", "Sienna", "Colton", "June", "Miles", "Cora", "Weston",
		"Lila", "Jaxon", "Piper", "Roman", "Sage", "Kai", "Ember", "Bennett",
		"Rose", "Felix", "Daisy", "Declan", "Autumn", "Silas", "Gemma", "Rowan",
		"Phoebe", "Archer", "Mila", "George", "Eliza", "Arthur",
	}
	macModels = []string{
		"Mac", "MacBook", "MacbookPro", "MacbookAir", "MacMini", "MacStudio",
	}
	linuxModels = []string{
		"Ubuntu Desktop", "Ubuntu Laptop", "Ubuntu Workstation",
	}
)

// DetectPlatform chooses the computer-name family. OPENOPQ_DEVICE_PLATFORM wins
// when it names linux or mac; otherwise a version string that mentions linux is
// treated as Linux, and everything else — including the 7.0.x Mac QQ builds —
// is Mac.
func DetectPlatform(version string) Platform {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OPENOPQ_DEVICE_PLATFORM"))) {
	case "linux":
		return PlatformLinux
	case "mac", "macos", "darwin":
		return PlatformMac
	}
	if strings.Contains(strings.ToLower(version), "linux") {
		return PlatformLinux
	}
	return PlatformMac
}

func NormalizePlatform(p Platform) Platform {
	if p == PlatformLinux {
		return PlatformLinux
	}
	return PlatformMac
}

// ComputerName is a stable display name derived from the 16-byte GUID. The same
// GUID and platform always produce the same string, so it can be persisted once
// and reused; later wordlist edits must not rewrite a saved profile.
func ComputerName(guid []byte, platform Platform) string {
	var seed [16]byte
	copy(seed[:], guid)
	n := binary.BigEndian.Uint64(seed[0:8])
	m := binary.BigEndian.Uint64(seed[8:16])
	models := macModels
	if NormalizePlatform(platform) == PlatformLinux {
		models = linuxModels
	}
	return englishGivenNames[n%uint64(len(englishGivenNames))] + "'s " + models[m%uint64(len(models))]
}
