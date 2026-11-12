package protocol

import (
	"fmt"
	"strings"
	"time"
)

// The status service.
//
// Two packets belong to it, and neither is an OIDB operation. A client announces itself with
// one of them, and then says it is still there with the other. The first is what makes the
// account reachable at all — a login that stops before it has signed in and nothing more —
// and it is also the step a connection repeats when it has to be made again, because the
// service forgets a client whose connection it lost.
//
// Both are built here from the reference's own definition of them. The announcement is a
// protobuf whose fields are named there. The other is one field with one value, which is
// carried as it is: there is nothing in it to name.
const (
	// RegisterCommand announces the client, and is what a login ends with.
	RegisterCommand = "trpc.qq_new_tech.status_svc.StatusService.Register"
	// SSOHeartbeatCommand reports that the client is still there. It is not the packet the
	// connection's keep-alive sends — that one goes to a service of its own and travels
	// unframed — but the protocol layer's own, which travels framed like a request.
	SSOHeartbeatCommand = "trpc.qq_new_tech.status_svc.StatusService.SsoHeartBeat"
	// SSOHeartbeatInterval is how long the reference waits between them. It is a cadence
	// rather than a limit, and it is long: the packet keeps a session from lapsing, and the
	// ten-second packet is what keeps the link itself alive.
	SSOHeartbeatInterval = 270 * time.Second

	// The announcement, field by field.
	registerGUIDField         = 1
	registerField2            = 2
	registerVersionField      = 3
	registerField4            = 4
	registerServiceIDField    = 5
	registerSystemField       = 6
	registerDeviceNameField   = 1
	registerKernelField       = 2
	registerSystemKernelField = 3
	registerField4OfSystem    = 4
	registerVendorOSField     = 5
	registerMuteField         = 7
	registerVendorTypeField   = 8
	registerTypeField         = 9
	// registerRegisterType is the value the last field carries. It is the only field whose
	// value differs from zero or from a plain description of the machine, and the reference
	// calls it the registration type without saying which type it is.
	registerRegisterType uint64 = 1
)

// RegisterInput is what the announcement is built from. Everything here is a name the client
// claims rather than something it proves, which is why it is all supplied.
type RegisterInput struct {
	// GUID identifies the install. It travels upper-cased, which is how the reference sends
	// it and the only thing about its case that is established.
	GUID string
	// Version is the client version, the same one the login's own blocks report.
	Version string
	// DeviceName, Kernel and SystemKernel describe the machine, and VendorOS the platform.
	DeviceName   string
	Kernel       string
	SystemKernel string
	VendorOS     string
	// ServiceID is the service the session belongs to.
	ServiceID uint32
}

// BuildRegister encodes the packet that announces the client.
//
// The two fields that carry nothing are written rather than left out. A protobuf encoder is
// free to omit a zero and this one does not, so omitting them here would send a different
// packet from the one the reference sends, and field order and presence are the whole of what
// can be compared until a capture of this packet exists.
func BuildRegister(in RegisterInput) []byte {
	return Marshal(
		StringField(registerGUIDField, strings.ToUpper(in.GUID)),
		VarintField(registerField2, 0),
		StringField(registerVersionField, in.Version),
		VarintField(registerField4, 0),
		VarintField(registerServiceIDField, uint64(in.ServiceID)),
		BytesField(registerSystemField, Marshal(
			StringField(registerDeviceNameField, in.DeviceName),
			StringField(registerKernelField, in.Kernel),
			StringField(registerSystemKernelField, in.SystemKernel),
			StringField(registerField4OfSystem, ""),
			StringField(registerVendorOSField, in.VendorOS),
		)),
		VarintField(registerMuteField, 0),
		VarintField(registerVendorTypeField, 0),
		VarintField(registerTypeField, registerRegisterType),
	)
}

// registerMessageField is where an answer says whether it accepted the announcement.
const registerMessageField = 2

// registerAccepted is what an answer that accepted it says, word for word.
const registerAccepted = "register success"

// ParseRegister reads an answer to the announcement.
//
// The answer is not a code this time: a service that accepts the client says so in words, and
// any other words are the refusal and are worth passing on as they were said.
func ParseRegister(answer []byte) error {
	// Like every SSO reply body, the register answer opens with a four-byte big-endian length
	// that counts itself, left in place by the frame layer for each command's parser to strip.
	// Without dropping it the protobuf reader reads that length's zero high byte as "field tag
	// 0" and fails (the same bug UnpackKeyExchange fixed).
	answer = stripSSOBodyLength(answer)
	fields, err := Unmarshal(answer)
	if err != nil {
		return fmt.Errorf("protocol: register answer: %w", err)
	}
	message, _ := fields[registerMessageField].([]byte)
	if string(message) == registerAccepted {
		return nil
	}
	if len(message) == 0 {
		return fmt.Errorf("protocol: the register answer says nothing")
	}
	return fmt.Errorf("protocol: the register answer refused: %s", message)
}

// BuildSSOHeartbeat encodes the packet that reports the client is still there.
//
// It carries one field with a value of one. Nothing observed says what the field is called,
// and the reference writes it as a single byte pair rather than as a named field, so this is
// the same two bytes built by the same encoder as everything else rather than a transcription.
func BuildSSOHeartbeat() []byte {
	return Marshal(VarintField(1, 1))
}
