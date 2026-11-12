package protocol

import (
	"fmt"
	"strconv"
	"strings"
)

// NTLoginCommand is the service the NT login is addressed to. It runs on the secret the
// key exchange established, and it is the path the client uses once it has one.
const NTLoginCommand = "trpc.login.ecdh.EcdhService.SsoNTLoginEasyLogin"

// Constants the NT login carries.
const (
	// ntPlatform is what the request reports as the operating system.
	ntPlatform = "Darwin"
	// ntDeviceMarker travels in the device block between the name and the guid.
	ntDeviceMarker = 5
	// ntClientName identifies the application.
	ntClientName = "com.tencent.qq"
	// ntRequestMarker is the request's last field.
	ntRequestMarker = 1
)

// Tags of an NT login message.
const (
	// Request and answer both use 1 and 2 for a header and a body, but the answer nests
	// them differently, so the two are named apart.
	ntRequestHeader = 1
	ntRequestBody   = 2

	// Inside the answer's outer message.
	ntResponseBlock = 3
	// Inside the answer's header.
	ntErrorBlock = 4
	// Inside the answer's body.
	ntCredentialBlock = 1
	// ntNoticeBlock holds wording the service adds beside a refusal.
	ntNoticeBlock  = 2
	ntAccountBlock = 4
	// Inside the error block.
	ntErrorCode = 1
	// Inside the account block.
	ntUidField = 2
	// Inside the credential block.
	ntCredential106  = 3
	ntCredential010A = 4
	ntCredential0143 = 5
	ntCredentialKey  = 6
)

// ntErrorTextFields are the numbers inside the error block that carry wording for a human.
var ntErrorTextFields = []int{2, 3, 5}

// ntRejectedWithoutCode stands in for the code when the service refuses without giving
// one. The reference uses -1 for this, and no real code is negative, so the case stays
// distinguishable from a refusal the server numbered zero.
const ntRejectedWithoutCode = -1

// NTLoginInput is what an NT login request is built from.
type NTLoginInput struct {
	Uin        uint32
	Guid       []byte
	SubAppID   uint32
	DeviceName string
	Cookies    string
	// Key, Token and Credential come from the key exchange and the scan. Key must be a
	// length AES accepts.
	Key        []byte
	Token      []byte
	Credential []byte
	// Nonce fills the enciphered block. An empty one is drawn from the system; supplying
	// one is for tests only.
	Nonce []byte
}

// Validate reports whether the input can produce a request at all.
func (in NTLoginInput) Validate() error {
	if in.Uin == 0 || in.SubAppID == 0 {
		return fmt.Errorf("%w: NT login needs an account and an application id",
			ErrMissingCredentials)
	}
	if len(in.Token) == 0 || len(in.Credential) == 0 {
		return fmt.Errorf("%w: NT login needs the scan ticket and the exchange token",
			ErrMissingCredentials)
	}
	if !isSSOKeySize(len(in.Key)) {
		return fmt.Errorf("%w: NT login key is %d bytes", ErrMissingCredentials, len(in.Key))
	}
	return nil
}

// BuildNTLogin encodes a request. The header and the credential travel together inside one
// enciphered block, so neither is readable on the wire.
func BuildNTLogin(input NTLoginInput) ([]byte, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	device := input.DeviceName
	if device == "" {
		device = platformName
	}
	header := Marshal(
		BytesField(ntRequestHeader, Marshal(
			BytesField(1, []byte(strconv.FormatUint(uint64(input.Uin), 10))))),
		BytesField(2, Marshal(
			StringField(1, platformName),
			StringField(2, device),
			VarintField(3, ntDeviceMarker),
			BytesField(4, input.Guid),
		)),
		BytesField(3, Marshal(
			StringField(1, ntPlatform),
			VarintField(2, uint64(input.SubAppID)),
			StringField(3, ntClientName),
		)),
		BytesField(5, Marshal(StringField(1, input.Cookies))),
	)
	plain := Marshal(
		BytesField(ntRequestHeader, header),
		BytesField(ntRequestBody, Marshal(BytesField(1, input.Credential))),
	)
	enciphered, err := sealWithGCM(plain, input.Key, input.Nonce)
	if err != nil {
		return nil, fmt.Errorf("encipher NT login: %w", err)
	}
	return Marshal(
		BytesField(ntRequestHeader, input.Token),
		BytesField(ntResponseBlock, enciphered),
		VarintField(4, ntRequestMarker),
	), nil
}

// UnpackNTLogin decodes the answer and recovers the session tickets.
//
// The response is enciphered with the key the exchange produced, so a key that never took
// part in the exchange cannot open it. That is what stops a captured answer from being
// replayed into another session, and it is why the key is a parameter here rather than
// being read from the packet.
func UnpackNTLogin(response, key []byte) (Credentials, error) {
	outer, err := Unmarshal(response)
	if err != nil {
		return Credentials{}, fmt.Errorf("NT login response: %w", err)
	}
	enciphered, err := bytesField(outer, ntResponseBlock)
	if err != nil {
		return Credentials{}, fmt.Errorf("NT login response: %w", err)
	}
	plain, err := openWithGCM(enciphered, key)
	if err != nil {
		return Credentials{}, fmt.Errorf("NT login response does not authenticate: %w", err)
	}
	base, err := Unmarshal(plain)
	if err != nil {
		return Credentials{}, fmt.Errorf("NT login contents: %w", err)
	}
	header, err := bytesField(base, ntRequestHeader)
	if err != nil {
		return Credentials{}, fmt.Errorf("NT login contents: %w", err)
	}
	body, err := bytesField(base, ntRequestBody)
	if err != nil {
		return Credentials{}, fmt.Errorf("NT login contents: %w", err)
	}

	code, pieces := ntError(header)
	bodyFields, err := Unmarshal(body)
	if err != nil {
		return Credentials{}, fmt.Errorf("NT login body: %w", err)
	}
	credentialBlock, credentialErr := bytesField(bodyFields, ntCredentialBlock)
	if code != 0 || credentialErr != nil || len(credentialBlock) == 0 {
		if notice, ok := bodyFields[ntNoticeBlock].([]byte); ok {
			if text := nestedText(notice, 3); text != "" {
				pieces = append(pieces, text)
			}
		}
		if code == 0 {
			code = ntRejectedWithoutCode
		}
		return Credentials{}, &LoginRejected{
			Code:    code,
			Message: "QQ rejected the NT login: " + strings.Join(pieces, " / "),
		}
	}

	credentialFields, err := Unmarshal(credentialBlock)
	if err != nil {
		return Credentials{}, fmt.Errorf("NT login credentials: %w", err)
	}
	credentials := Credentials{}
	for tag, value := range credentialFields {
		data, ok := value.([]byte)
		if !ok {
			continue
		}
		applyNTCredential(&credentials, tag, data)
	}
	if !credentials.Complete() {
		return Credentials{}, fmt.Errorf("NT login response did not supply a complete ticket set")
	}
	if accountBlock, ok := bodyFields[ntAccountBlock].([]byte); ok {
		if account, err := Unmarshal(accountBlock); err == nil {
			if uid, ok := account[ntUidField].([]byte); ok {
				credentials.Uid = string(uid)
			}
		}
	}
	return credentials, nil
}

// ntError reads the result code and any wording for a human out of the header's error
// block. A header with no error block at all reports no error, which is the same thing the
// reference concludes from an empty message.
//
// Wording fields that are present but empty are dropped: they carry no information, and
// keeping them would put bare separators in the message.
func ntError(header []byte) (int, []string) {
	fields, err := Unmarshal(header)
	if err != nil {
		return 0, nil
	}
	block, ok := fields[ntErrorBlock].([]byte)
	if !ok {
		return 0, nil
	}
	errorFields, err := Unmarshal(block)
	if err != nil {
		return 0, nil
	}
	code := 0
	if number, ok := errorFields[ntErrorCode].(uint64); ok {
		code = int(number)
	}
	pieces := make([]string, 0, len(ntErrorTextFields))
	for _, tag := range ntErrorTextFields {
		if text, ok := errorFields[tag].([]byte); ok && len(text) > 0 {
			pieces = append(pieces, string(text))
		}
	}
	return code, pieces
}

// nestedText picks a text field out of a protobuf message, for the one place the wording
// sits a level deeper than the rest.
func nestedText(message []byte, tag int) string {
	fields, err := Unmarshal(message)
	if err != nil {
		return ""
	}
	text, ok := fields[tag].([]byte)
	if !ok {
		return ""
	}
	return string(text)
}

// applyNTCredential files one credential under the ticket it belongs to. The field numbers
// differ from the ones a WTLOGIN response uses, so the two cannot share a mapping.
func applyNTCredential(credentials *Credentials, tag int, value []byte) {
	switch tag {
	case ntCredential106:
		credentials.Token0106 = copyBytes(value)
	case ntCredential010A:
		credentials.Token010A = copyBytes(value)
	case ntCredential0143:
		credentials.Token0143 = copyBytes(value)
	case ntCredentialKey:
		credentials.SessionKey = copyBytes(value)
	}
}
