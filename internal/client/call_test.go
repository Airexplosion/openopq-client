package client

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"openopq/internal/bot"
	"openopq/internal/protocol"
)

const groupQuery = "OidbSvcTrpcTcp.0xfe1_2"

// An operation is addressed by name and carries a body, and the answer comes back in the same
// envelope — which is what a caller needs to drive one whose own encoding is not known here.
func TestCallSendsAnOIDBRequest(t *testing.T) {
	b, fake := connectedBot(t)
	request := protocol.OIDBRequest{
		Command: protocol.OIDBCommand{Service: 0xFE1, SubCommand: 2},
		Body:    []byte{0x0A, 0x03, 0x01, 0x02, 0x03},
	}
	answer := protocol.OIDBRequest{Command: request.Command, Body: []byte{0x12, 0x01, 0x7F}}
	// The fake answers with a reply's body, which for an OIDB call is the envelope itself.
	fake.replies = append(fake.replies, protocol.BuildOIDB(answer))

	params, err := json.Marshal(map[string]any{"body": hex.EncodeToString(request.Body)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result, err := b.Call(context.Background(), groupQuery, params)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	// What went out is the envelope, and it is addressed to the service the name gave.
	if len(fake.calls) != 1 || fake.calls[0] != groupQuery {
		t.Fatalf("the request went to %v, want %s", fake.calls, groupQuery)
	}
	sent, err := protocol.UnwrapOIDB(fake.sent[0])
	if err != nil {
		t.Fatalf("what went out is not an envelope: %v", err)
	}
	if sent.Command != request.Command || !bytes.Equal(sent.Body, request.Body) {
		t.Errorf("what went out was %#v", sent)
	}

	// And what came back is the answer's body, as hex.
	fields, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("the result is %T", result)
	}
	if fields["body"] != hex.EncodeToString(answer.Body) {
		t.Errorf("the answer body is %v, want %s", fields["body"], hex.EncodeToString(answer.Body))
	}
}

// The real service exchange returns the inclusive SSO body length too. A fake that only
// returned bare OIDB envelopes hid this difference between unit tests and network replies.
func TestCallReadsAnSSOFramedOIDBAnswer(t *testing.T) {
	b, fake := connectedBot(t)
	want := protocol.OIDBRequest{
		Command:  protocol.OIDBCommand{Service: 0xFE1, SubCommand: 2},
		Body:     []byte{0x12, 0x01, 0x7F},
		Reserved: 1,
	}
	fake.replies = append(fake.replies, protocol.LV32(protocol.BuildOIDB(want)))
	result, err := b.Call(context.Background(), groupQuery, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	fields, ok := result.(map[string]any)
	if !ok || fields["body"] != hex.EncodeToString(want.Body) || fields["reserved"] != want.Reserved {
		t.Fatalf("framed answer decoded as %#v", result)
	}
}

// An answer from a service the caller did not address is not an answer to its request.
func TestCallRejectsAnAnswerFromAnotherService(t *testing.T) {
	b, fake := connectedBot(t)
	fake.replies = append(fake.replies, protocol.BuildOIDB(protocol.OIDBRequest{
		Command: protocol.OIDBCommand{Service: 0xFD4, SubCommand: 1},
		Body:    []byte{1},
	}))
	if _, err := b.Call(context.Background(), groupQuery, nil); err == nil {
		t.Error("an answer from another service was accepted")
	}
}

// Only OIDB services are addressed this way, and a name that is not one has to be refused
// rather than sent somewhere unintended.
func TestCallRejectsNamesThatAreNotOIDB(t *testing.T) {
	b, _ := connectedBot(t)
	for _, name := range []string{"wtlogin.login", "Heartbeat.Alive", "", "OidbSvcTrpcTcp.0xfe1"} {
		if _, err := b.Call(context.Background(), name, nil); err == nil {
			t.Errorf("%q was accepted", name)
		}
	}
}

func TestCallRejectsABodyThatIsNotHex(t *testing.T) {
	b, _ := connectedBot(t)
	if _, err := b.Call(context.Background(), groupQuery, json.RawMessage(`{"body":"not hex"}`)); err == nil {
		t.Error("a body that is not hex was accepted")
	}
}

func TestCallBeforeThereIsASession(t *testing.T) {
	b := New(BotOptions{Device: referenceDevice()})
	if _, err := b.Call(context.Background(), groupQuery, nil); !errors.Is(err, bot.ErrNotConnected) {
		t.Errorf("Call gave %v, want ErrNotConnected", err)
	}
}

// groupsAnswer wraps a list of groups the way the service does: the envelope, and inside it
// the repeated field, one entry per group.
func groupsAnswer(entries ...[]byte) []byte {
	var body []byte
	for _, entry := range entries {
		body = append(body, protocol.Marshal(protocol.BytesField(2, entry))...)
	}
	return protocol.BuildOIDB(protocol.OIDBRequest{
		Command: protocol.OIDBCommand{Service: 0xFE5, SubCommand: 2},
		Body:    body,
	})
}

// groupEntry is one group as the service reports it.
func groupEntry(uin uint32, name string, memberCount uint32) []byte {
	return protocol.Marshal(
		protocol.VarintField(3, uint64(uin)),
		protocol.BytesField(4, protocol.Marshal(
			protocol.VarintField(4, uint64(memberCount)),
			protocol.StringField(5, name),
		)),
	)
}

// The list is asked for, not waited on, and the answer is handed back in the shape the API
// layer reports.
func TestGroupsAsksForTheList(t *testing.T) {
	b, fake := connectedBot(t)
	fake.replies = append(fake.replies, groupsAnswer(
		groupEntry(10001, "the first", 12),
		groupEntry(10002, "the second", 3),
	))

	groups, err := b.Groups(context.Background())
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(fake.calls) != 1 || fake.calls[0] != protocol.FetchGroupsCommand {
		t.Fatalf("the request went to %v, want %s", fake.calls, protocol.FetchGroupsCommand)
	}
	if !bytes.Equal(fake.sent[0], protocol.BuildFetchGroups()) {
		t.Error("what went out is not a fetch")
	}
	if len(groups) != 2 {
		t.Fatalf("read %d groups, want 2", len(groups))
	}
	if groups[0].GroupUin != 10001 || groups[0].GroupName != "the first" || groups[0].MemberCount != 12 {
		t.Errorf("the first group came back as %+v", groups[0])
	}
}

func TestGroupsReadsAnSSOFramedOIDBAnswer(t *testing.T) {
	b, fake := connectedBot(t)
	fake.replies = append(fake.replies, protocol.LV32(groupsAnswer(groupEntry(10001, "synthetic group", 12))))
	groups, err := b.Groups(context.Background())
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(groups) != 1 || groups[0].GroupUin != 10001 || groups[0].GroupName != "synthetic group" {
		t.Fatalf("framed groups decoded as %#v", groups)
	}
}

// An account with no groups is a real answer and comes back as an empty list, not as an error.
func TestGroupsAcceptsAnEmptyList(t *testing.T) {
	b, fake := connectedBot(t)
	fake.replies = append(fake.replies, groupsAnswer())
	groups, err := b.Groups(context.Background())
	if err != nil {
		t.Fatalf("an empty list was refused: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("an empty list read as %d groups", len(groups))
	}
}

// A refusal is a failure, and reporting it as a list of nothing would tell the caller the
// account has no groups — which is the one thing it does not mean.
func TestGroupsReportsARefusal(t *testing.T) {
	b, fake := connectedBot(t)
	fake.replies = append(fake.replies, protocol.Marshal(
		protocol.VarintField(1, 0xFE5),
		protocol.VarintField(2, 2),
		protocol.VarintField(3, 1200),
		protocol.StringField(5, "no such account"),
	))
	if _, err := b.Groups(context.Background()); err == nil {
		t.Fatal("a refusal was reported as a group list")
	}
}

func TestGroupsBeforeThereIsASession(t *testing.T) {
	b := New(BotOptions{Device: referenceDevice()})
	if _, err := b.Groups(context.Background()); !errors.Is(err, bot.ErrNotConnected) {
		t.Errorf("Groups gave %v, want ErrNotConnected", err)
	}
}

// An operation that failed says so inside the envelope. Its body would be empty, and handing
// that back would leave the caller unable to tell a failure from an operation with nothing to
// say.
func TestCallReportsARefusalFromTheEnvelope(t *testing.T) {
	b, fake := connectedBot(t)
	fake.replies = append(fake.replies, protocol.Marshal(
		protocol.VarintField(1, 0xFE1),
		protocol.VarintField(2, 2),
		protocol.VarintField(3, 45),
		protocol.StringField(5, "please sign in again"),
	))
	_, err := b.Call(context.Background(), groupQuery, nil)
	if err == nil {
		t.Fatal("an operation the service refused came back as a result")
	}
	if !strings.Contains(err.Error(), "please sign in again") {
		t.Errorf("the service's own words were lost: %v", err)
	}
}
