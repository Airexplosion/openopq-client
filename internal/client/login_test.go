package client

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"openopq/internal/protocol"
)

const scanUin = 1234567890

// loginReply builds an answer to a login. A successful one carries its credential block
// enciphered with the device key; a refused one explains itself instead.
//
// The code is the result byte the answer opens with, which is what a caller sees — the
// reference codec reports the same one, and the wording that goes with it comes from the
// detail block beside it.
func loginReply(t *testing.T, shareKey, deviceKey []byte, code byte) []byte {
	t.Helper()
	var blocks []protocol.TLVItem
	if code == 0 {
		credentials := protocol.WriteTLVs([]protocol.TLVItem{
			{Tag: 0x305, Value: bytes.Repeat([]byte{0x5E}, protocol.SessionKeySize)},
			{Tag: 0x10A, Value: bytes.Repeat([]byte{0xA1}, 16)},
			{Tag: 0x143, Value: bytes.Repeat([]byte{0xD2}, 16)},
			{Tag: 0x106, Value: bytes.Repeat([]byte{0x06}, 16)},
			{Tag: 0x543, Value: protocol.Marshal(protocol.BytesField(9, protocol.Marshal(
				protocol.BytesField(11, protocol.Marshal(protocol.BytesField(1, []byte("uid_1")))))))},
		})
		enciphered, err := protocol.Encrypt(credentials, deviceKey)
		if err != nil {
			t.Fatalf("encipher the credentials: %v", err)
		}
		blocks = []protocol.TLVItem{{Tag: 0x119, Value: enciphered}}
	} else {
		detail := append(protocol.U32(uint32(code)), protocol.LV16([]byte("禁止登录"))...)
		detail = append(detail, protocol.LV16([]byte("请升级"))...)
		blocks = []protocol.TLVItem{{Tag: 0x146, Value: detail}}
	}
	plain := append(append(protocol.U16(9), code), protocol.WriteTLVs(blocks)...)
	return replyFrame(t, shareKey, plain)
}

// scanned is what a confirmation looks like once it has been read.
func scanned(deviceKey []byte) ScanResult {
	return ScanResult{
		Uin:       scanUin,
		DeviceKey: deviceKey,
		Ticket:    bytes.Repeat([]byte{0x07}, 88),
	}
}

func loginMachine(t *testing.T, script ...[]byte) (*QRLogin, *fakeExchange) {
	t.Helper()
	fake := &fakeExchange{}
	machine, err := NewQRLogin(fake, referenceDevice(), instantOptions())
	if err != nil {
		t.Fatalf("NewQRLogin: %v", err)
	}
	fake.key = machine.shareKey
	for _, payload := range script {
		fake.replies = append(fake.replies, replyFrame(t, machine.shareKey, payload))
	}
	return machine, fake
}

func TestLoginReturnsTheCredentials(t *testing.T) {
	deviceKey := bytes.Repeat([]byte{0x5A}, 16)
	fake := &fakeExchange{}
	machine, err := NewQRLogin(fake, referenceDevice(), instantOptions())
	if err != nil {
		t.Fatalf("NewQRLogin: %v", err)
	}
	fake.key = machine.shareKey
	fake.replies = append(fake.replies, loginReply(t, machine.shareKey, deviceKey, 0))

	credentials, err := machine.Login(context.Background(), scanned(deviceKey),
		LoginInput{Ticket16A: bytes.Repeat([]byte{0x16}, 88)})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if len(credentials.SessionKey) != protocol.SessionKeySize {
		t.Errorf("the session key is %d bytes", len(credentials.SessionKey))
	}
	if !bytes.Equal(credentials.Token010A, bytes.Repeat([]byte{0xA1}, 16)) {
		t.Errorf("the ticket is %x", credentials.Token010A)
	}
	if !bytes.Equal(credentials.Token0143, bytes.Repeat([]byte{0xD2}, 16)) {
		t.Errorf("the second ticket is %x", credentials.Token0143)
	}
	if credentials.Uid != "uid_1" {
		t.Errorf("uid = %q, want uid_1", credentials.Uid)
	}
	if !credentials.Complete() {
		t.Error("the credentials from a successful login report themselves incomplete")
	}
	if fake.logins != 1 {
		t.Errorf("the service was asked to log in %d times, want 1", fake.logins)
	}
}

// A login's frame carries the login's own command, which is two below a code fetch's. The
// number is the only thing that tells the service which of the two it is looking at.
func TestLoginFramesWithTheLoginCommand(t *testing.T) {
	deviceKey := bytes.Repeat([]byte{0x5A}, 16)
	fake := &fakeExchange{}
	machine, err := NewQRLogin(fake, referenceDevice(), instantOptions())
	if err != nil {
		t.Fatalf("NewQRLogin: %v", err)
	}
	fake.key = machine.shareKey
	fake.replies = append(fake.replies, loginReply(t, machine.shareKey, deviceKey, 0))

	if _, err := machine.Login(context.Background(), scanned(deviceKey),
		LoginInput{Ticket16A: bytes.Repeat([]byte{0x16}, 88)}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("the fake saw %d requests", len(fake.sent))
	}
	// What the exchange is handed is the frame, which is also what the signer is given; the
	// length the service layer adds is added after both.
	frame := fake.sent[0]
	header := protocol.NewReader(frame[3:])
	if _, err := header.U16(); err != nil { // the client version
		t.Fatalf("header: %v", err)
	}
	command, err := header.U16()
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if command != protocol.OICQLoginCommand {
		t.Errorf("the frame carries command %d, want %d", command, protocol.OICQLoginCommand)
	}
}

// A refusal is the service's answer and has to reach the caller as one.
func TestLoginSurfacesARefusal(t *testing.T) {
	deviceKey := bytes.Repeat([]byte{0x5A}, 16)
	fake := &fakeExchange{}
	machine, err := NewQRLogin(fake, referenceDevice(), instantOptions())
	if err != nil {
		t.Fatalf("NewQRLogin: %v", err)
	}
	fake.key = machine.shareKey
	fake.replies = append(fake.replies, loginReply(t, machine.shareKey, deviceKey, 45))

	credentials, err := machine.Login(context.Background(), scanned(deviceKey),
		LoginInput{Ticket16A: bytes.Repeat([]byte{0x16}, 88)})
	var rejected *protocol.LoginRejected
	if !errors.As(err, &rejected) {
		t.Fatalf("error is %v, want *protocol.LoginRejected", err)
	}
	if rejected.Code != 45 {
		t.Errorf("code = %d, want 45", rejected.Code)
	}
	if !bytes.Contains([]byte(rejected.Message), []byte("禁止登录")) {
		t.Errorf("the refusal lost its wording: %v", rejected)
	}
	if len(credentials.SessionKey) != 0 {
		t.Error("a refusal produced credentials")
	}
}

// A login without the account or the device key cannot be built at all, and saying so is
// better than sending something the service will reject for a different reason.
func TestLoginNeedsTheScanYield(t *testing.T) {
	deviceKey := bytes.Repeat([]byte{0x5A}, 16)
	for _, check := range []struct {
		name string
		scan ScanResult
	}{
		{"no account", ScanResult{DeviceKey: deviceKey}},
		{"no device key", ScanResult{Uin: scanUin}},
		{"nothing at all", ScanResult{}},
	} {
		machine, _ := loginMachine(t)
		if _, err := machine.Login(context.Background(), check.scan,
			LoginInput{Ticket16A: []byte{1}}); err == nil {
			t.Errorf("%s: accepted", check.name)
		}
	}
}

// The login's own blocks have to be there: the service reads them, and a login that leaves
// them out is a login the service will refuse for a reason that has nothing to do with the
// scan.
func TestLoginNeedsTheCredentialBlocks(t *testing.T) {
	deviceKey := bytes.Repeat([]byte{0x5A}, 16)
	scan := scanned(deviceKey)
	scan.Ticket = nil // nothing to put in the first block
	machine, _ := loginMachine(t)
	if _, err := machine.Login(context.Background(), scan, LoginInput{Ticket16A: []byte{1}}); err == nil {
		t.Error("a login with no credential block was built")
	}
}
