package protocol

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

// The OIDB envelope.
//
// Every OIDB service travels in the same wrapper: a service number, one of its sub-commands,
// the service's own body, and a reserved field. Both numbers also appear in the name a client
// gives the service — OidbSvcTrpcTcp.0xfe1_2 is service 0xfe1 and sub-command 2 — so the
// name is not opaque and parsing it is how a caller reaches an operation it can name.
//
// The layout was read off requests the client sent, captured as they were signed, and all
// eighteen agree. Their bodies are readable too, which is why the envelope is worth having
// on its own: it is the part every operation shares, so an operation only has to supply what
// is inside it.
//
// A published implementation's own definition of this wrapper agrees field for field, and it
// is what named the two fields a capture of requests could not show. An answer carries an
// error code and a message beside the body, which a request leaves out; and the last field is
// a reserved integer that the implementation sets from a flag meaning "this request is
// addressed by UID" — which is what the ones and zeros in the captures are.
const (
	oidbServiceField      = 1
	oidbSubCommandField   = 2
	oidbErrorCodeField    = 3
	oidbBodyField         = 4
	oidbErrorMessageField = 5
	oidbReservedField     = 12
	// OIDBNamePrefix begins the name of every OIDB service.
	OIDBNamePrefix = "OidbSvcTrpcTcp."
)

// OIDBCommand names an OIDB service: the service, and which of its sub-commands.
type OIDBCommand struct {
	Service    uint32
	SubCommand uint32
}

// ParseOIDBCommand reads the name a client gives an OIDB service.
func ParseOIDBCommand(name string) (OIDBCommand, error) {
	rest, ok := strings.CutPrefix(name, OIDBNamePrefix)
	if !ok {
		return OIDBCommand{}, fmt.Errorf("protocol: %q is not an OIDB service name", name)
	}
	service, subCommand, found := strings.Cut(rest, "_")
	if !found {
		return OIDBCommand{}, fmt.Errorf("protocol: %q names no sub-command", name)
	}
	serviceNumber, err := strconv.ParseUint(service, 0, 32)
	if err != nil {
		return OIDBCommand{}, fmt.Errorf("protocol: %q has no service number: %w", name, err)
	}
	subCommandNumber, err := strconv.ParseUint(subCommand, 0, 32)
	if err != nil {
		return OIDBCommand{}, fmt.Errorf("protocol: %q has no sub-command number: %w", name, err)
	}
	return OIDBCommand{Service: uint32(serviceNumber), SubCommand: uint32(subCommandNumber)}, nil
}

// String renders the command the way a client names it, which is also the service a request
// is addressed to.
func (c OIDBCommand) String() string {
	return fmt.Sprintf("%s%#x_%d", OIDBNamePrefix, c.Service, c.SubCommand)
}

// OIDBRequest is a request: which service, what it is told, and the reserved field beside
// them. It serves as the reading of an answer too, because an answer wears the same wrapper
// and only adds to it.
type OIDBRequest struct {
	Command OIDBCommand
	Body    []byte
	// Reserved is the envelope's last field. It travels both ways and is not derived here:
	// ten of the captured requests carry zero there and eight carry one, which the published
	// definition explains as whether the operation is addressed by UID.
	Reserved uint64
	// ErrorCode and ErrorMessage are an answer's, and are zero on a request. A non-zero code
	// is a service reporting that it did not carry the operation out, and it is what tells an
	// empty body from a body that says nothing.
	ErrorCode    uint32
	ErrorMessage string
}

// Failed reports whether an answer carried an error rather than a result.
func (r OIDBRequest) Failed() bool { return r.ErrorCode != 0 }

// BuildOIDB wraps a request in the envelope.
func BuildOIDB(request OIDBRequest) []byte {
	return Marshal(
		VarintField(oidbServiceField, uint64(request.Command.Service)),
		VarintField(oidbSubCommandField, uint64(request.Command.SubCommand)),
		BytesField(oidbBodyField, request.Body),
		VarintField(oidbReservedField, request.Reserved),
	)
}

// UnwrapOIDB reads an envelope and hands back the service and its body. It serves answers as
// well as requests: a service answers in the same wrapper, which is what lets a caller read
// one without knowing anything about the other.
//
// A non-zero error code is reported in the result rather than as a failure, because it is a
// reading of the packet like any other and some callers want the code itself.
// An SSO response still carries its body's four-byte inclusive length. Accept exactly one
// complete length-prefixed body as well as the bare envelopes used when building requests;
// a mismatched length is not a reason to discard bytes or ignore trailing data.
func UnwrapOIDB(plain []byte) (OIDBRequest, error) {
	if len(plain) >= 4 && uint64(binary.BigEndian.Uint32(plain[:4])) == uint64(len(plain)) {
		plain = plain[4:]
	}
	fields, err := Unmarshal(plain)
	if err != nil {
		return OIDBRequest{}, fmt.Errorf("OIDB envelope: %w", err)
	}
	service, ok := fields[oidbServiceField].(uint64)
	if !ok {
		return OIDBRequest{}, fmt.Errorf("OIDB envelope names no service")
	}
	subCommand, ok := fields[oidbSubCommandField].(uint64)
	if !ok {
		return OIDBRequest{}, fmt.Errorf("OIDB envelope names no sub-command")
	}
	body, err := bytesField(fields, oidbBodyField)
	if err != nil {
		// A body may be absent, which an empty answer does; an envelope that carries a body
		// of another shape is the error worth reporting.
		if _, present := fields[oidbBodyField]; present {
			return OIDBRequest{}, fmt.Errorf("OIDB envelope body: %w", err)
		}
		body = nil
	}
	errorCode, _ := fields[oidbErrorCodeField].(uint64)
	errorMessage, _ := fields[oidbErrorMessageField].([]byte)
	reserved, _ := fields[oidbReservedField].(uint64)
	return OIDBRequest{
		Command:      OIDBCommand{Service: uint32(service), SubCommand: uint32(subCommand)},
		Body:         copyBytes(body),
		Reserved:     reserved,
		ErrorCode:    uint32(errorCode),
		ErrorMessage: string(errorMessage),
	}, nil
}
