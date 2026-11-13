package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReplySegmentIsKeptWhenNative(t *testing.T) {
	got, err := validatedOBMessage(json.RawMessage(`[{"type":"reply","data":{"id":"123"}},{"type":"text","data":{"text":"1d100=42"}}]`), false, true)
	if err != nil {
		t.Fatalf("native reply + text refused: %v", err)
	}
	if got != "[CQ:reply,id=123]1d100=42" {
		t.Fatalf("got %q, want the quote CQ plus text", got)
	}
}

func TestReplySegmentRejectedWithoutNativeBackend(t *testing.T) {
	if _, err := validatedOBMessage(json.RawMessage(`[{"type":"reply","data":{"id":"123"}},{"type":"text","data":{"text":"1d100=42"}}]`), false); err == nil {
		t.Fatal("reply must not be accepted without a native backend")
	}
}

func TestOtherUnknownSegmentsAreStillRefused(t *testing.T) {
	if _, err := validatedOBMessage(json.RawMessage(`[{"type":"music","data":{"id":"1"}}]`), false); err == nil {
		t.Fatal("music segment should be refused")
	}
}

func TestNativeReplyPlusRecordIsAccepted(t *testing.T) {
	got, err := validatedOBMessage(json.RawMessage(`[{"type":"reply","data":{"id":"123"}},{"type":"record","data":{"file":"base64://eA=="}}]`), false, true)
	if err != nil {
		t.Fatalf("native reply + record refused: %v", err)
	}
	if !strings.Contains(got, "[CQ:reply,id=123]") || !strings.Contains(got, "[CQ:record,") {
		t.Fatalf("got %q", got)
	}
	if _, err := validatedOBMessage(json.RawMessage(`[{"type":"reply","data":{"id":"123"}},{"type":"record","data":{"file":"base64://eA=="}}]`), false); err == nil {
		t.Fatal("reply + record must not be accepted without a native backend")
	}
	if _, err := validatedOBMessage(json.RawMessage(`[{"type":"record","data":{"file":"base64://eA=="}},{"type":"text","data":{"text":"hi"}}]`), false, true); err == nil {
		t.Fatal("record + text must still be refused")
	}
}
