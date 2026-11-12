package protocol

// SessionKeySize is the length of the session key a login must yield. The service refuses
// a session built on anything else, so the decoder checks it here rather than leaving a
// malformed key to fail somewhere less obvious.
const SessionKeySize = 16

// Credentials are the tickets a successful login yields.
//
// Every field is a private copy: the decoder never hands back a slice of the packet it
// read, so a caller may hold these while the receive buffer is reused for the next
// packet. The names follow the wire tags, because that is what the captured traffic and
// the reference codec call them and a friendlier name would only be a guess at what each
// ticket is for.
type Credentials struct {
	Token0106  []byte
	Token010A  []byte
	Token0143  []byte
	SessionKey []byte
	Token0133  []byte
	Token0134  []byte
	Token0114  []byte
	Token0318  []byte
	// Skey and Uid are optional: the service does not send them on every login.
	Skey string
	Uid  string
	// NickName and Age come from the profile block, which is optional as well.
	NickName string
	Age      byte
}

// Complete reports whether the credentials can actually hold a session. A response that
// reports success but leaves any of the three out is not usable, and the decoder treats
// it as a failure rather than handing back half a session.
func (c Credentials) Complete() bool {
	return len(c.Token010A) != 0 && len(c.Token0143) != 0 && len(c.SessionKey) == SessionKeySize
}

// copyBytes detaches a value from the packet it was read out of.
func copyBytes(value []byte) []byte { return append([]byte(nil), value...) }
