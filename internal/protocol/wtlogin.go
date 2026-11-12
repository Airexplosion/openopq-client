package protocol

import (
	"errors"
	"fmt"
)

// WTLoginCommand is the service a login packet is addressed to.
const WTLoginCommand = "wtlogin.login"

// Constants the login body carries. They are protocol values, not tunables: the client
// version and signature map are what the server checks the request against.
const (
	// clientVersionField is the account block's (0x18) version word. A real 7.0.2 client
	// writes 8001 here (0x1f41), not 13172 — captured from QQ702's own wtlogin.login.
	clientVersionField uint32 = 13172 // 6.9.95 reference (protocol.py 0x18); was 8001 for 7.0.2
	// appClientVersion is the application block's (0x100) version word. A real 7.0.2 client
	// writes 13172 (0x3374) here — the two words are the reverse of the 6.9.95 reference.
	appClientVersion uint32 = 8001 // 6.9.95 reference (protocol.py 0x100); was 13172 for 7.0.2
	mainSigMap       uint32 = 169742560
	// compatibilityBitmap travels in its own block.
	compatibilityBitmap uint32 = 12058620
	// ssoVersion is the service version the client speaks.
	ssoVersion uint32 = 5
	// packageName identifies the application.
	packageName = "com.tencent.qq"
	// platformName is what the device block reports for this build.
	platformName = "Mac"
	// sdkVersion travels in the SDK block.
	sdkVersion = "nt.wtlogin.0.0.1"
	// sdkBuildMarker is the single byte the SDK block starts with.
	sdkBuildMarker = 5
	// loginBodyMarker precedes the tag count; every reference packet has it set to 9.
	loginBodyMarker uint16 = 9
	// ssoVersionField is the service version in the capability block (0x521). A real 7.0.2
	// client writes 5 here, not 0x13 — captured from QQ702's own wtlogin.login.
	ssoVersionField uint32 = 0x13 // 6.9.95 reference (protocol.py 0x521 = 19); was 5 for 7.0.2
	// capabilities are the flags the capability block advertises.
	capabilities = "basicim"
	// unknownDeviceFlags travel with the device block's device list entry.
	unknownDeviceFlags = 7
	unknownDeviceName  = "Unknown"
	// miscBitmap travels in the block the protocol puts first after the ticket.
	miscBitmap uint32 = 5
	// ticketFlags is the two-byte prefix on the ticket block.
	ticketFlags uint16 = 1
	// ticketMarker is the byte following the ticket flags.
	ticketMarker = 0x0D
)

// TgtgtKeySize is the length of the key that protects the device block.
const TgtgtKeySize = 16

// ErrMissingCredentials reports a login attempt without the scan credentials the server
// requires. Callers surface it to the user as "scan again" rather than retrying.
var ErrMissingCredentials = errors.New("protocol: login needs scan credentials")

// WTLoginInput is everything a login body is built from.
type WTLoginInput struct {
	Uin uint32
	// AppID and SubAppID are the two application identifiers; the original code names
	// them the other way round, so they are passed explicitly here.
	AppID    uint32
	SubAppID uint32
	Guid     []byte
	// DeviceName is what the device block reports.
	DeviceName string
	// Ticket106, Ticket16A and TgtgtKey come from the scan. TgtgtKey must be 16 bytes.
	Ticket106 []byte
	Ticket16A []byte
	TgtgtKey  []byte
}

// Validate reports whether the input can produce a login body at all.
func (in WTLoginInput) Validate() error {
	if in.Uin == 0 {
		return fmt.Errorf("%w: no account", ErrMissingCredentials)
	}
	if len(in.Ticket106) == 0 || len(in.Ticket16A) == 0 || len(in.TgtgtKey) != TgtgtKeySize {
		return fmt.Errorf("%w: ticket 0x106, ticket 0x16A or the device key is missing",
			ErrMissingCredentials)
	}
	return nil
}

// BuildWTLogin encodes the login body.
//
// The device block is enciphered with the key from the scan, so its ciphertext differs
// between runs even for identical input; every other block is fixed by the input.
func BuildWTLogin(input WTLoginInput) ([]byte, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	device, err := Encrypt(deviceBlock(input), input.TgtgtKey)
	if err != nil {
		return nil, err
	}

	items := []TLVItem{
		{Tag: 0x106, Value: input.Ticket106},
		{Tag: 0x144, Value: device},
		{Tag: 0x116, Value: append(append(append([]byte{0}, U32(compatibilityBitmap)...), U32(0)...), 0)},
		{Tag: 0x142, Value: append(U16(0), LV16([]byte(packageName))...)},
		{Tag: 0x145, Value: input.Guid},
		{Tag: 0x18, Value: accountBlock(input.Uin)},
		{Tag: 0x141, Value: append(append(U32(unknownDeviceFlags), []byte(unknownDeviceName)...), U32(0)...)},
		{Tag: 0x177, Value: append(append([]byte{1}, U32(0)...), LV16([]byte(sdkVersion))...)},
		{Tag: 0x191, Value: []byte{0}},
		{Tag: 0x100, Value: applicationBlock(input)},
		{Tag: 0x107, Value: append(append(append(U16(ticketFlags), byte(ticketMarker)), U16(0)...), 1)},
		{Tag: 0x318, Value: []byte{}},
		{Tag: 0x16A, Value: input.Ticket16A},
		{Tag: 0x166, Value: []byte{sdkBuildMarker}},
		{Tag: 0x521, Value: append(U32(ssoVersionField), LV16([]byte(capabilities))...)},
	}

	body := make([]byte, 0, 512)
	body = append(body, U16(loginBodyMarker)...)
	return append(body, WriteTLVs(items)...), nil
}

// accountBlock carries the version and account the request is for.
func accountBlock(uin uint32) []byte {
	block := make([]byte, 0, 20)
	block = append(block, U16(0)...)
	block = append(block, U32(ssoVersion)...)
	block = append(block, U32(0)...)
	block = append(block, U32(clientVersionField)...)
	block = append(block, U32(uin)...)
	block = append(block, U16(0)...)
	return append(block, U16(0)...)
}

// applicationBlock describes the client build.
func applicationBlock(input WTLoginInput) []byte {
	block := make([]byte, 0, 24)
	block = append(block, U16(0)...)
	block = append(block, U32(ssoVersion)...)
	// SubAppID comes first on the wire; the original code names the two the other way
	// round, which is why this looks inverted.
	block = append(block, U32(input.SubAppID)...)
	block = append(block, U32(input.AppID)...)
	block = append(block, U32(appClientVersion)...)
	return append(block, U32(mainSigMap)...)
}

// deviceBlock describes the device. It is the one block that is enciphered.
func deviceBlock(input WTLoginInput) []byte {
	device := input.DeviceName
	if device == "" {
		device = platformName
	}
	block := make([]byte, 0, 128)
	block = append(block, U16(4)...)
	block = append(block, TLV(0x16e, []byte(device))...)
	block = append(block, TLV(0x147, applicationNameBlock(input))...)
	block = append(block, TLV(0x128, deviceListBlock(input, device))...)
	return append(block, TLV(0x124, make([]byte, 12))...)
}

func applicationNameBlock(input WTLoginInput) []byte {
	block := make([]byte, 0, 32)
	block = append(block, U32(input.SubAppID)...)
	block = append(block, LV16([]byte("2.0.0"))...)
	return append(block, LV16([]byte(packageName))...)
}

func deviceListBlock(input WTLoginInput, device string) []byte {
	block := make([]byte, 0, 64)
	block = append(block, U16(0)...)
	// Device-availability flags. A real 7.0.2 client writes 0,0,0 here (captured from QQ702's
	// own wtlogin.login device block); the 6.9.95 reference wrote 0,1,0.
	block = append(block, 0, 1, 0) // 6.9.95 reference (protocol.py 0x128); was 0,0,0 for 7.0.2
	block = append(block, U32(0)...)
	block = append(block, LV16([]byte(platformName))...)
	block = append(block, LV16(input.Guid)...)
	return append(block, LV16(nil)...)
}
