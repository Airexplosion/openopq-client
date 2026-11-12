package protocol

import "fmt"

// LoginRejected reports that the login service refused the request.
//
// It is a distinct type rather than a plain error because callers have to tell a refusal
// apart from a transport failure. A refusal means the credentials are spent and the
// account has to be scanned again; a transport failure means sending the same request
// once more is reasonable. Code carries the result number the server sent so a caller can
// branch on it without reading the message. It is an int and not a byte because one path
// reports a refusal that carries no code at all, and that case has to stay distinguishable
// from a refusal the server numbered zero.
type LoginRejected struct {
	Code    int
	Message string
}

func (e *LoginRejected) Error() string { return e.Message }

// refusal turns a non-zero result code into an error, borrowing the server's own wording
// when the packet carries any.
func refusal(code byte, items map[uint16][]byte) error {
	message := fmt.Sprintf("QQ rejected the login with code %d", code)
	for _, detail := range []struct {
		tag  uint16
		skip int
	}{{tagRejectDetail, 4}, {tagRejectDetailAlt, 2}} {
		value, ok := items[detail.tag]
		if !ok {
			continue
		}
		first, second, err := readDetailPair(value, detail.skip)
		if err != nil {
			continue
		}
		message += ": " + first + " / " + second
		break
	}
	if notice, ok := items[tagRejectNotice]; ok {
		message += " " + string(notice)
	}
	return &LoginRejected{Code: int(code), Message: message}
}

// readDetailPair reads the two strings a rejection detail block holds behind a
// fixed-size prefix. What the prefix means is not established; the reference skips it,
// so this does too rather than guessing at its contents.
func readDetailPair(value []byte, skip int) (string, string, error) {
	reader := NewReader(value)
	if _, err := reader.Take(skip); err != nil {
		return "", "", err
	}
	first, err := reader.LV16()
	if err != nil {
		return "", "", err
	}
	second, err := reader.LV16()
	if err != nil {
		return "", "", err
	}
	return string(first), string(second), nil
}
