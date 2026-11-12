package client

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// This is a relay milestone, not proof that the SDK accepted or stored a token.
func o3SecureAccessCommand(command string) bool {
	switch command {
	case "trpc.o3.ecdh_access.EcdhAccess.SsoSecureAccess",
		"trpc.o3.ecdh_access.EcdhAccess.SsoSecureA2Establish",
		"trpc.o3.ecdh_access.EcdhAccess.SsoSecureA2Access":
		return true
	default:
		return false
	}
}

// Preserve callback IDs as exact JSON, including int64 IDs encoded as strings by JS.
// Converting through float64 would silently route some responses to another ID.
type o3Pull struct {
	Empty  bool            `json:"empty"`
	Subseq json.RawMessage `json:"subseq"`
	Cmd    string          `json:"cmd"`
	Body   string          `json:"body"`
}

// o3ReplyPayload converts ServiceExchange's inclusive SSO body into the payload
// consumed by libsecO3OnReceive. Unlike command-specific parsers, the SDK's O3
// outer decoder starts at byte zero and expects the length prefix to be absent.
func o3ReplyPayload(body []byte) ([]byte, error) {
	if len(body) < 4 {
		return nil, fmt.Errorf("O3 reply is missing its inclusive SSO length")
	}
	declared := uint64(binary.BigEndian.Uint32(body[:4]))
	if declared != uint64(len(body)) {
		return nil, fmt.Errorf("O3 reply has invalid inclusive SSO length")
	}
	return body[4:], nil
}

func readO3Pull(reader io.Reader) (o3Pull, []byte, error) {
	var value o3Pull
	decoder := json.NewDecoder(io.LimitReader(reader, 256*1024+1))
	if err := decoder.Decode(&value); err != nil {
		return value, nil, fmt.Errorf("invalid O3 response JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return value, nil, fmt.Errorf("extra O3 response data")
	}
	if value.Empty {
		if value.Cmd != "" || len(value.Subseq) != 0 || value.Body != "" {
			return value, nil, fmt.Errorf("conflicting O3 empty response")
		}
		return value, nil, nil
	}
	if value.Cmd == "" || len(value.Cmd) >= 256 || strings.ContainsRune(value.Cmd, 0) {
		return value, nil, fmt.Errorf("invalid O3 command")
	}
	id := string(value.Subseq)
	if strings.HasPrefix(id, "\"") {
		if err := json.Unmarshal(value.Subseq, &id); err != nil {
			return value, nil, fmt.Errorf("invalid O3 callback ID")
		}
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != id {
		return value, nil, fmt.Errorf("invalid O3 callback ID")
	}
	if len(value.Body) > 128*1024 {
		return value, nil, fmt.Errorf("O3 request too large")
	}
	body, err := hex.DecodeString(value.Body)
	if err != nil || len(body) == 0 {
		return value, nil, fmt.Errorf("invalid O3 request body")
	}
	return value, body, nil
}
