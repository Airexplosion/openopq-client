package api

import (
	"encoding/json"
	"testing"
)

func TestReplySegmentIsSentWithoutTheQuote(t *testing.T) {
	got, err := validatedOBMessage(json.RawMessage(`[{"type":"reply","data":{"id":"123"}},{"type":"text","data":{"text":"1d100=42"}}]`), false)
	if err != nil {
		t.Fatalf("reply + text refused: %v", err)
	}
	if got != "1d100=42" {
		t.Fatalf("got %q, want the text alone", got)
	}
}

func TestReplySegmentAloneIsStillEmpty(t *testing.T) {
	if _, err := validatedOBMessage(json.RawMessage(`[{"type":"reply","data":{"id":"123"}}]`), false); err == nil {
		t.Fatal("a message that is only a quote must not be sent as an empty message")
	}
}

func TestOtherUnknownSegmentsAreStillRefused(t *testing.T) {
	if _, err := validatedOBMessage(json.RawMessage(`[{"type":"music","data":{"id":"1"}}]`), false); err == nil {
		t.Fatal("music segment should be refused")
	}
}
