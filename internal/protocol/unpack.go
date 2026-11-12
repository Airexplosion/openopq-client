package protocol

import "fmt"

// LoginResponseMarker is the value a login response opens with. Every reference packet
// carries it set to 9, the same value the request body opens with.
const LoginResponseMarker uint16 = 9

// Tags in a login response, at the top level.
const (
	// tagCredentialBlock holds the enciphered credential list. It is the one block a
	// successful response must carry.
	tagCredentialBlock uint16 = 0x119
	// tagRejectDetail and tagRejectDetailAlt are the two ways a refusal explains itself:
	// a pair of strings behind a fixed-size prefix. In practice exactly one is present.
	tagRejectDetail    uint16 = 0x146
	tagRejectDetailAlt uint16 = 0x149
	// tagRejectNotice is a bare string some refusals carry instead of, or alongside, the
	// pair above.
	tagRejectNotice uint16 = 0x192
)

// Tags inside the enciphered credential block.
const (
	tagTicket106  uint16 = 0x106
	tagTicket010A uint16 = 0x10A
	tagTicket0114 uint16 = 0x114
	tagTicket0133 uint16 = 0x133
	tagTicket0134 uint16 = 0x134
	tagTicket0143 uint16 = 0x143
	tagTicket0318 uint16 = 0x318
	tagSessionKey uint16 = 0x305
	tagProfile    uint16 = 0x11A
	tagSkey       uint16 = 0x120
	tagUid        uint16 = 0x543
)

// uidFields is the path the account id sits at inside its block: three protobuf messages
// nested nine, eleven and one.
var uidFields = []int{9, 11, 1}

// UnpackWTLogin decodes the answer to a login request.
//
// The input is the response body with the OICQ frame already off it and its outer layer
// deciphered, which leaves a marker, a one-byte result code, and a TLV list. A non-zero
// code is a refusal and comes back as a *LoginRejected. A zero code must be followed by
// the credential block, enciphered with the same device key the request carried.
func UnpackWTLogin(body, tgtgtKey []byte) (Credentials, error) {
	reader := NewReader(body)
	if _, err := reader.U16(); err != nil {
		return Credentials{}, fmt.Errorf("login response marker: %w", err)
	}
	code, err := reader.U8()
	if err != nil {
		return Credentials{}, fmt.Errorf("login response result code: %w", err)
	}
	items, err := ReadTLVs(body[reader.Pos():])
	if err != nil {
		return Credentials{}, fmt.Errorf("login response blocks: %w", err)
	}
	if code != 0 {
		return Credentials{}, refusal(code, items)
	}

	enciphered, ok := items[tagCredentialBlock]
	if !ok {
		return Credentials{}, fmt.Errorf("login response reports success but carries no credential block")
	}
	plain, err := Decrypt(enciphered, tgtgtKey)
	if err != nil {
		return Credentials{}, fmt.Errorf("credential block: %w", err)
	}
	fields, err := ReadTLVs(plain)
	if err != nil {
		return Credentials{}, fmt.Errorf("credential block contents: %w", err)
	}
	credentials := credentialsFrom(fields)
	if !credentials.Complete() {
		return Credentials{}, fmt.Errorf("login response did not supply a complete ticket set")
	}
	return credentials, nil
}

// credentialsFrom collects the credential fields. Values are copied rather than aliased,
// so the result outlives the packet.
//
// The profile and account-id blocks are read after the loop: both are optional and both
// can be malformed, and neither is worth failing a login that has already produced the
// tickets a session needs.
func credentialsFrom(fields map[uint16][]byte) Credentials {
	credentials := Credentials{}
	var profile, uid []byte
	for tag, value := range fields {
		switch tag {
		case tagTicket106:
			credentials.Token0106 = copyBytes(value)
		case tagTicket010A:
			credentials.Token010A = copyBytes(value)
		case tagTicket0114:
			credentials.Token0114 = copyBytes(value)
		case tagTicket0133:
			credentials.Token0133 = copyBytes(value)
		case tagTicket0134:
			credentials.Token0134 = copyBytes(value)
		case tagTicket0143:
			credentials.Token0143 = copyBytes(value)
		case tagTicket0318:
			credentials.Token0318 = copyBytes(value)
		case tagSessionKey:
			credentials.SessionKey = copyBytes(value)
		case tagSkey:
			credentials.Skey = string(value)
		case tagProfile:
			profile = value
		case tagUid:
			uid = value
		}
	}
	credentials.NickName, credentials.Age = readProfile(profile)
	credentials.Uid = readUid(uid)
	return credentials
}

// readProfile reads the nickname and age block. A block that does not parse yields no
// profile rather than an error, because the reference ignores one too.
func readProfile(block []byte) (string, byte) {
	reader := NewReader(block)
	if _, err := reader.U16(); err != nil {
		return "", 0
	}
	age, err := reader.U8()
	if err != nil {
		return "", 0
	}
	if _, err := reader.U8(); err != nil {
		return "", 0
	}
	size, err := reader.U8()
	if err != nil {
		return "", 0
	}
	nickname, err := reader.Take(int(size))
	if err != nil {
		return "", 0
	}
	return string(nickname), age
}

// readUid digs the account id out of its block, following the nesting recorded in
// uidFields. Anything that does not match the expected shape yields no account id.
func readUid(block []byte) string {
	current := block
	for _, tag := range uidFields {
		message, err := Unmarshal(current)
		if err != nil {
			return ""
		}
		next, err := bytesField(message, tag)
		if err != nil {
			return ""
		}
		current = next
	}
	return string(current)
}

// bytesField picks a length-delimited field out of a decoded protobuf message.
func bytesField(message map[int]any, tag int) ([]byte, error) {
	value, ok := message[tag]
	if !ok {
		return nil, fmt.Errorf("protocol: protobuf message has no field %d", tag)
	}
	data, ok := value.([]byte)
	if !ok {
		return nil, fmt.Errorf("protocol: protobuf field %d is not length-delimited", tag)
	}
	return data, nil
}
