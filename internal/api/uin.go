package api

import (
	"fmt"
	"strconv"
)

// parseUint reads an account id. The protocol carries it as a decimal string, and a
// malformed one is a caller error rather than a zero account.
func parseUint(value string) (uint32, error) {
	if value == "" {
		return 0, fmt.Errorf("empty uin")
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid uin %q: %w", value, err)
	}
	return uint32(parsed), nil
}
