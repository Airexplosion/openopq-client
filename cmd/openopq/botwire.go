package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"openopq/internal/client"
	"openopq/internal/config"
	"openopq/internal/device"
	"openopq/internal/protocol"
	"openopq/internal/sign"
	"openopq/internal/transport"
)

// The application identity the QR flow speaks for.
//
// The identity is a pair: one number that is the same on every platform and every build, and
// one that changes with the build. Sending one build's second number while claiming another
// build's version is the kind of disagreement a service refuses without saying why, so the
// two are looked up together by the build the configuration names.
//
// The numbers are read off the clients themselves: 537391652 is what a 7.0.2 client's own
// `wtlogin.trans_emp` carries, and 537352498 is what OPQ's account state reports while it
// claims 6.9.95.
const (
	qrSubAppID   uint32 = 1600001602 // the platform's half, 0x5f5e1642
	qrMainSigMap uint32 = 169742560
	qrSSOVersion uint32 = 5
	qrMsfID      uint32 = 2052
	// qrPTOSVersion is the value fetch TLVs 0x35 and 0x66 carry. A real 7.0.2 macOS client's
	// own fetch (deciphered from a capture) sends 5 here — not the 23 a published macOS profile
	// would suggest. Read off the client, this is what it sends.
	qrPTOSVersion uint32 = 5
)

// qrAppIDFor is the build's half of the identity, by the build.
var qrAppIDFor = map[string]uint32{
	"6.9.95-48517": 537352498, // 0x20075932
	"6.9.96-49738": 537352498,
	"7.0.0-52194":  537379435, // 0x2007c26b — read off QQ700 (7.0.0-52194)'s own SSO header;
	// a genuine 7.0.0 login + register (Online:true) uses this, NOT the 6.9.95 537352498 that
	// was wrongly shared here before.
	"7.0.2-53644": 537391652, // 0x2007f224 — read off QQ702 (7.0.2-53644)'s own SSO header in
	// a live trans_emp capture. An earlier change to 0x2007c26b was a mistake: that value came
	// from QQ700, which is 7.0.0-52194, not 7.0.2, so sending it while claiming 7.0.2 (QUA and
	// PTVersion both 7.0.2) made the request internally inconsistent and the server dropped it.
}

// qrAppID is the build's half for a version, falling back to the one every build but the
// newest shares.
func qrAppID(version string) uint32 {
	if text := strings.TrimSpace(os.Getenv("OPENOPQ_APPID")); text != "" {
		if v, err := strconv.ParseUint(text, 10, 32); err == nil {
			return uint32(v)
		}
	}
	if id, ok := qrAppIDFor[version]; ok {
		return id
	}
	return 537352498
}

// ssoAppID is the application id the SSO header reports, which a capture shows differs from
// the one the device blocks carry. OPENOPQ_SSO_APPID overrides it for a deployment that has
// read its own off a client.
func ssoAppID(fallback uint32) uint32 {
	if text := strings.TrimSpace(os.Getenv("OPENOPQ_SSO_APPID")); text != "" {
		if v, err := strconv.ParseUint(text, 10, 32); err == nil {
			return uint32(v)
		}
	}
	return fallback
}

const qrPackageName = "com.tencent.qq"

// defaultNTSign is the value every request's extension carries in field 15.
//
// It is not derived and not per-account: OPQ ships this exact string as a constant in its
// binary, and the account state it logged for a session that reached the logged-in stage
// carried the same one across 6.9.95, 6.9.96, 7.0.0 and 7.0.2. So it behaves as a protocol
// constant of this ecosystem rather than a device identity, and sending it is what the one
// implementation that logs in does. OPENOPQ_NTSIGN overrides it for a deployment that has its
// own.
const defaultNTSign = "00-4140d146e41521c4febdf8bccdb0c891-bfbe9808724056e9-01"

// ntSign returns the configured NTSign, falling back to the shipped default.
func ntSign() string {
	if value := os.Getenv("OPENOPQ_NTSIGN"); value != "" {
		return value
	}
	return defaultNTSign
}

// defaultServer is the chat server the protocol connects to.
//
// It is deliberately not one of the configuration's CDN lists. Those name content servers —
// the same document carries a separate list of downloadable plugins beside them — and the
// address the connection goes to is the client's own, which is this one: two independent
// implementations of the protocol and the client's own record of itself all say so, and a
// survey of the clients running here says the same. A deployment that needs another names it
// in the environment.
const defaultServer = "msfwifi.3g.qq.com:8080"

// serverAddress is where the protocol connection goes.
func serverAddress() string {
	if address := strings.TrimSpace(os.Getenv("OPENOPQ_SERVER")); address != "" {
		return address
	}
	return defaultServer
}

// buildBot wires the framework's interface to the protocol implementation.
//
// A device's identity has to be the same across runs — the service notices when it is not,
// and a client that looks like a new machine every time is one that gets asked to prove
// itself. So the parts that identify the device come from the environment when they are set,
// and are drawn and reported when they are not.
func buildBot(loaded *config.Config, logger *slog.Logger) (*client.Bot, error) {
	guid, err := deviceGUID(logger)
	if err != nil {
		return nil, err
	}
	return buildBotWithDevice(loaded, logger, guid, 0, "")
}

func computerNameFor(store *device.Store, guid []byte, uin uint32) string {
	if store != nil && uin != 0 {
		if p, err := store.Peek(strconv.FormatUint(uint64(uin), 10)); err == nil && p.ComputerName != "" {
			return p.ComputerName
		}
	}
	platform := device.PlatformMac
	if store != nil {
		platform = store.Platform()
	}
	return device.ComputerName(guid, platform)
}

func protocolMachine(platform device.Platform) (osName, kernel, vendor string) {
	if platform == device.PlatformLinux {
		return "Linux", "Linux", "linux"
	}
	return "Mac", "Darwin", "mac"
}

// Account profiles supply their own identity; process-wide legacy GUID overrides
// must never collapse different accounts onto one device.
func buildBotWithDevice(loaded *config.Config, logger *slog.Logger, guid []byte, expectedUin uint32, computerName string, stateDirectories ...string) (*client.Bot, error) {
	version := clientVersion(loaded.Version)
	platform := device.DetectPlatform(version)
	if computerName == "" {
		computerName = device.ComputerName(guid, platform)
	}
	osName, kernel, vendor := protocolMachine(platform)
	appID := qrAppID(version)
	address := serverAddress()
	logger.Info("connecting to the chat server", "address", address,
		"cdn_count", len(loaded.CDNLists), "computer_name", computerName, "device_platform", string(platform))

	signer := sign.New(loaded.SignEndpoint(), 30*time.Second)
	if len(stateDirectories) > 0 {
		endpointHash := sha256.Sum256([]byte(loaded.SignEndpoint()))
		signer.SetStatePath(filepath.Join(stateDirectories[0], hex.EncodeToString(guid)+"-"+hex.EncodeToString(endpointHash[:8])+".enc"))
	}
	return client.New(client.BotOptions{
		Logger:         logger,
		Reconnect:      client.ReconnectOptions{Disabled: loaded.AutoReconnect != nil && !*loaded.AutoReconnect},
		DisableO3Relay: os.Getenv("OPENOPQ_RESEARCH_NO_O3") == "1",
		ExpectedUin:    expectedUin,
		Dial: func(ctx context.Context) (client.Connection, error) {
			return transport.Dial(ctx, address, transport.Options{Logger: logger})
		},
		Signer: signer,
		Device: protocol.QRDevice{
			AppID:       appID,
			SubAppID:    qrSubAppID,
			MainSigMap:  qrMainSigMap,
			SSOVersion:  qrSSOVersion,
			PTOSVersion: qrPTOSVersion,
			PackageName: qrPackageName,
			PTVersion:   versionTag(loaded.Version),
			// TLV 0xD1 field 1 is the signer platform ("Mac" or "Linux"). Darwin/Linux
			// belong in Register.Kernel, not here.
			OS: osName,
			// DeviceName is the persisted computer name. A real 7.0.2 Mac fetch used
			// "macOS" here; QQ's device list shows the possessive name instead.
			DeviceName: computerName,
			Guid:       guid,
		},
		// The announcement a login ends with. Kernel and VendorOS stay the signer
		// platform's constants; DeviceName is the same persisted computer name the
		// QR block reports, so the machine looks like one install.
		Register: protocol.RegisterInput{
			Version:    version,
			DeviceName: computerName,
			Kernel:     kernel,
			VendorOS:   vendor,
		},
		// The keep-alive reports an installation identity, and a capture shows what it is:
		// "Ib " followed by thirty-two hex digits, which are the same sixteen bytes the
		// device's other blocks carry. So it is derived from the device rather than handed in,
		// and a deployment that has some other identity can still name it.
		Token: heartbeatToken(guid),
		Session: client.Session{
			AppID:   ssoAppID(appID),
			MsfID:   qrMsfID,
			NTSign:  ntSign(),
			Framing: framingFor(loaded.Version),
		},
	}), nil
}

// framingFor defaults to the classic framing exercised by this implementation's
// QR login, including 7.0.x identities. The alternate native capture format remains
// available for research through an explicit environment override.
func framingFor(version string) protocol.Framing {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OPENOPQ_FRAMING"))) {
	case "classic":
		return protocol.FramingClassic
	case "seven":
		return protocol.FramingSeven
	}
	return protocol.FramingClassic
}

// deviceGUID is the identifier every request's header and every device block reports, which
// is to say the one identifier the client has. A client whose header and blocks disagreed
// would be claiming to be two machines, and the service remembers the one it was told about
// first.
func deviceGUID(logger *slog.Logger) ([]byte, error) {
	if text := strings.TrimSpace(os.Getenv("OPENOPQ_GUID")); text != "" {
		raw, err := hex.DecodeString(text)
		if err != nil {
			return nil, fmt.Errorf("OPENOPQ_GUID is not hex: %w", err)
		}
		if len(raw) != 16 {
			return nil, fmt.Errorf("OPENOPQ_GUID is %d bytes, want 16", len(raw))
		}
		return raw, nil
	}
	// No override: keep a per-install device id in a file so it stays stable across runs.
	// First run draws a random one and saves it; later runs reuse it. The file can be moved
	// with OPENOPQ_GUID_FILE; each deployment/directory thus gets its own device identity.
	path := strings.TrimSpace(os.Getenv("OPENOPQ_GUID_FILE"))
	if path == "" {
		path = "openopq-device.guid"
	}
	if data, err := os.ReadFile(path); err == nil {
		if raw, err := hex.DecodeString(strings.TrimSpace(string(data))); err == nil && len(raw) == 16 {
			logger.Info("device guid loaded", "file", path, "guid", hex.EncodeToString(raw))
			return raw, nil
		}
		logger.Warn("device guid file is invalid; drawing a new one", "file", path)
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("draw a device guid: %w", err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(raw)), 0o600); err != nil {
		logger.Warn("could not save device guid; it will change next run", "file", path, "error", err)
	} else {
		logger.Info("generated and saved a new device guid (reused on later runs)",
			"file", path, "guid", hex.EncodeToString(raw))
	}
	return raw, nil
}

// heartbeatToken is what a keep-alive reports as the installation's own identity, derived from
// the device unless a deployment names another.
func heartbeatToken(guid []byte) string {
	if text := strings.TrimSpace(os.Getenv("OPENOPQ_TOKEN")); text != "" {
		return text
	}
	return protocol.HeartbeatToken(guid)
}

// clientVersion is the version the announcement claims, taken from the identity the
// configuration names: "QQNT-v7.0.0-52194" announces "7.0.0-52194". It keeps the build
// number, which is what tells the version the client's own block reports — "7.0.0" — apart
// from the one it is.
func clientVersion(identity string) string {
	return strings.TrimPrefix(identity, "QQNT-v")
}

// versionTag is the client version the QR flow's blocks report, taken from the identity the
// configuration names: "QQNT-v7.0.0-52194" reports "7.0.0".
func versionTag(identity string) string {
	tag := strings.TrimPrefix(identity, "QQNT-v")
	if cut := strings.LastIndex(tag, "-"); cut > 0 {
		tag = tag[:cut]
	}
	return tag
}
