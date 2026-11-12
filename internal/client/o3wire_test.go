package client

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

func TestO3ReplyStripsOneCompleteInclusiveSSOLength(t *testing.T) {
	payload := []byte{0x08, 0x01, 0x12, 0x02, 0xaa, 0xbb}
	framed := make([]byte, 4, 4+len(payload))
	binary.BigEndian.PutUint32(framed, uint32(4+len(payload)))
	framed = append(framed, payload...)
	got, err := o3ReplyPayload(framed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %x, want %x", got, payload)
	}
}

func TestO3ReplyRejectsIncompleteOrTrailingSSOFrame(t *testing.T) {
	for _, body := range [][]byte{
		{}, {0, 0, 0}, {0, 0, 0, 9, 0x08}, {0, 0, 0, 5, 0x08, 0x01},
	} {
		if _, err := o3ReplyPayload(body); err == nil {
			t.Fatalf("accepted malformed O3 SSO frame %x", body)
		}
	}
}

func TestO3CallbackIDSurvivesJSONRoundTripExactly(t *testing.T) {
	for _, id := range []string{"9223372036854775807", `"9223372036854775807"`, "-9223372036854775808", "9007199254740993"} {
		pull, body, err := readO3Pull(strings.NewReader(`{"cmd":"trpc.o3.test","body":"0801","subseq":` + id + `}`))
		if err != nil || !bytes.Equal(body, []byte{8, 1}) {
			t.Fatalf("decode: %v", err)
		}
		feed, err := json.Marshal(map[string]any{"subseq": pull.Subseq})
		if err != nil || string(feed) != `{"subseq":`+id+`}` {
			t.Fatalf("ID changed: %s %v", feed, err)
		}
	}
}

func TestO3PullDoesNotTreatMalformedOrExtraDataAsEmpty(t *testing.T) {
	for _, input := range []string{
		`{}`, `{"empty":true,"cmd":"other"}`, `{"empty":true} {}`,
		`{"cmd":"test","body":"08","subseq":9223372036854775808}`,
		`{"cmd":"test","body":"08","subseq":1.0}`,
		`{"cmd":"test","body":"08","subseq":"01"}`,
		`{"cmd":"test","body":"zz","subseq":1}`,
		`{"cmd":"test","body":"","subseq":1}`,
	} {
		if _, _, err := readO3Pull(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	if pull, _, err := readO3Pull(strings.NewReader(`{"empty":true}`)); err != nil || !pull.Empty {
		t.Fatal("valid empty response rejected")
	}
}
