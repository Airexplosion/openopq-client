package client

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"openopq/internal/protocol"
)

// fakeExchange stands in for the wire: it opens each request far enough to see which
// sub-command it carries, and answers with a scripted reply.
//
// It reads the machine's agreed key, which is what lets it do that at all — a service could,
// because the key is agreed with it. Nothing else about the machine is reachable from here.
type fakeExchange struct {
	key     []byte
	replies [][]byte
	asked   []uint32
	calls   []string
	logins  int
	sent    [][]byte
	err     error
}

func (f *fakeExchange) Exchange(ctx context.Context, command string, body []byte) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	switch command {
	case protocol.TransEmpCommand:
		// What arrives here is the frame itself, which is also what the signer is given: the
		// length the service layer adds is the service layer's, and it is added after this.
		// UnwrapOICQ deciphers the body once, which yields the plaintext record — the record
		// travels enciphered exactly once, so there is no second layer to peel.
		packet, err := protocol.UnwrapOICQ(body, f.key)
		if err != nil {
			return nil, err
		}
		subCommand, err := requestSubCommand(packet)
		if err != nil {
			return nil, err
		}
		f.asked = append(f.asked, subCommand)
	case LoginCommand:
		if _, err := protocol.UnwrapOICQ(body, f.key); err != nil {
			return nil, err
		}
		f.logins++
		f.sent = append(f.sent, append([]byte(nil), body...))
	default:
		// An OIDB request is addressed by name and carries nothing else, so the fake only
		// has to note what it was asked and answer.
		f.calls = append(f.calls, command)
		f.sent = append(f.sent, append([]byte(nil), body...))
	}

	if len(f.replies) == 0 {
		return nil, errors.New("the script ran out of replies")
	}
	next := f.replies[0]
	f.replies = f.replies[1:]
	return next, nil
}

// requestSubCommand reads the sub-command out of a request. A request and a reply are not
// the same shape — the reply carries a different outer layer — so this walks the request's
// own: the layer around the record, then the record's start, its head length and the
// sub-command.
func requestSubCommand(packet []byte) (uint32, error) {
	reader := protocol.NewReader(packet)
	if _, err := reader.Take(1 + 2 + 4 + 4 + 2 + 1); err != nil {
		return 0, err
	}
	if _, err := reader.U32(); err != nil { // the timestamp
		return 0, err
	}
	if _, err := reader.U8(); err != nil { // the record's start marker
		return 0, err
	}
	if _, err := reader.U16(); err != nil { // the head length
		return 0, err
	}
	subCommand, err := reader.U16()
	if err != nil {
		return 0, err
	}
	return uint32(subCommand), nil
}

// replyFrame wraps a payload the way a service does. The frame's header is written but never
// read: the client steps over it, which is all it can do without knowing the build.
func replyFrame(t *testing.T, key, payload []byte) []byte {
	t.Helper()
	enciphered, err := protocol.Encrypt(payload, key)
	if err != nil {
		t.Fatalf("encipher: %v", err)
	}
	frame := []byte{0x02, 0, 0}
	total := 3 + 13 + len(enciphered) + 1
	frame[1], frame[2] = byte(total>>8), byte(total)
	frame = append(frame, 0x1F, 0x41, 0x08, 0x12)
	frame = append(frame, make([]byte, 9)...)
	frame = append(frame, enciphered...)
	frame = append(frame, 0x03)
	return protocol.LV32(frame)
}

// transEmpReply wraps a sub-command's payload in the layer the flow puts around it.
func transEmpReply(subCommand uint32, appID uint32, payload []byte) []byte {
	out := protocol.U32(uint32(len(payload) + 54))
	out = append(out, protocol.U32(0)...)
	out = append(out, protocol.U16(uint16(subCommand))...)
	out = append(out, make([]byte, 40)...)
	out = append(out, protocol.U32(appID)...)
	return append(out, payload...)
}

// fetchPayload is a fetch's answer, wrapped the way the flow wraps one.
func fetchPayload(t *testing.T, url string) []byte {
	t.Helper()
	code := protocol.Marshal(
		protocol.BytesField(1, protocol.Marshal(protocol.StringField(1, url))),
		protocol.BytesField(2, []byte{0x30, 0x01}),
	)
	payload := append([]byte{0x00}, protocol.LV16([]byte{0xAA, 0xBB})...)
	payload = append(payload, protocol.WriteTLVs([]protocol.TLVItem{
		{Tag: protocol.QRCodeTag, Value: code},
		{Tag: protocol.QRReplyImageTag, Value: []byte{0x89, 0x50}},
	})...)
	return transEmpReply(protocol.QRSubCommandFetch, referenceDevice().AppID, payload)
}

// scanPayload is a poll's answer. A device key of nothing means a poll that has nothing to
// report, which is what most of them are.
func scanPayload(state byte, uin uint32, deviceKey []byte) []byte {
	payload := append([]byte{state}, protocol.U32(0)...)
	if len(deviceKey) == 0 {
		return transEmpReply(protocol.QRSubCommandPoll, referenceDevice().AppID, payload)
	}
	payload = append(payload, protocol.U32(uin)...)
	payload = append(payload, protocol.U32(0)...)
	payload = append(payload, protocol.WriteTLVs([]protocol.TLVItem{
		{Tag: protocol.QRConfirmTicketTag, Value: []byte{0x01}},
		{Tag: protocol.QRConfirmNoPicSigTag, Value: bytes.Repeat([]byte{0x19}, 40)},
		{Tag: protocol.QRConfirmDeviceKeyTag, Value: deviceKey},
	})...)
	return transEmpReply(protocol.QRSubCommandPoll, referenceDevice().AppID, payload)
}

func referenceDevice() protocol.QRDevice {
	return protocol.QRDevice{
		AppID:       537352498,
		SubAppID:    1600001602,
		MainSigMap:  169742560,
		SSOVersion:  5,
		PackageName: "com.tencent.qq",
		PTVersion:   "7.0.2",
		OS:          "Mac",
		DeviceName:  "Mac",
		Guid:        bytes.Repeat([]byte{0x11}, 16),
	}
}

// instantOptions make the machine's waits do nothing, so a test does not sit out the code's
// real lifetime.
func instantOptions() QRLoginOptions {
	return QRLoginOptions{
		Poll:     time.Millisecond,
		Lifetime: time.Minute,
		Sleep:    func(context.Context, time.Duration) error { return nil },
	}
}

func newMachine(t *testing.T, script ...[]byte) (*QRLogin, *fakeExchange) {
	t.Helper()
	fake := &fakeExchange{}
	machine, err := NewQRLogin(fake, referenceDevice(), instantOptions())
	if err != nil {
		t.Fatalf("NewQRLogin: %v", err)
	}
	fake.key = machine.shareKey
	for _, payload := range script {
		fake.replies = append(fake.replies, replyFrame(t, machine.shareKey, payload))
	}
	return machine, fake
}

const referenceURL = "https://txz.qq.com/p?k=example&f=1600001602"

func TestFetchReturnsTheCode(t *testing.T) {
	machine, fake := newMachine(t, fetchPayload(t, referenceURL))

	code, err := machine.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if code.URL != referenceURL {
		t.Errorf("url = %q, want %q", code.URL, referenceURL)
	}
	if !bytes.Equal(code.Signature, []byte{0xAA, 0xBB}) {
		t.Errorf("signature = %x", code.Signature)
	}
	if !bytes.Equal(code.Image, []byte{0x89, 0x50}) {
		t.Errorf("image = %x", code.Image)
	}
	// The request the machine sent has to have been a fetch, which is what the fake could
	// read because it holds the key the two sides agreed.
	if len(fake.asked) != 1 || fake.asked[0] != protocol.QRSubCommandFetch {
		t.Errorf("the machine asked for %v, want one fetch", fake.asked)
	}
}

func TestQRWaitRetriesOnlyTransientPollTimeouts(t *testing.T) {
	machine, fake := newMachine(t, scanPayload(0x00, 1234567890, bytes.Repeat([]byte{0x11}, 16)))
	calls := 0
	machine.exchange = obQueryExchange(func(ctx context.Context, command string, body []byte) ([]byte, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("poll has no timeout")
		}
		if calls < 3 {
			return nil, context.DeadlineExceeded
		}
		return fake.Exchange(ctx, command, body)
	})
	result, err := machine.Wait(context.Background(), QRCode{Signature: []byte{1}})
	if err != nil || result.Uin != 1234567890 || calls != 3 {
		t.Fatalf("transient poll recovery: calls=%d result=%+v err=%v", calls, result, err)
	}
	for _, failure := range []error{context.DeadlineExceeded, errors.New("authentication refused")} {
		calls = 0
		machine.exchange = obQueryExchange(func(context.Context, string, []byte) ([]byte, error) { calls++; return nil, failure })
		_, err = machine.Wait(context.Background(), QRCode{Signature: []byte{1}})
		if err == nil {
			t.Fatal("error hidden")
		}
		want := 1
		if errors.Is(failure, context.DeadlineExceeded) {
			want = 3
		}
		if calls != want {
			t.Fatalf("unexpected retries: %d want %d", calls, want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	machine.exchange = obQueryExchange(func(context.Context, string, []byte) ([]byte, error) { calls++; return nil, context.DeadlineExceeded })
	if _, err = machine.Wait(ctx, QRCode{Signature: []byte{1}}); !errors.Is(err, context.Canceled) || calls > 1 {
		t.Fatal("parent cancellation retried")
	}
}

func TestWaitPollsUntilTheScanIsConfirmed(t *testing.T) {
	deviceKey := bytes.Repeat([]byte{0x5A}, 16)
	machine, fake := newMachine(t,
		fetchPayload(t, referenceURL),
		scanPayload(0x01, 0, nil),
		scanPayload(0x02, 0, nil),
		scanPayload(0x00, 1234567890, deviceKey),
	)

	code, err := machine.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	result, err := machine.Wait(context.Background(), code)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.Uin != 1234567890 {
		t.Errorf("account = %d", result.Uin)
	}
	if !bytes.Equal(result.DeviceKey, deviceKey) {
		t.Errorf("device key = %x, want %x", result.DeviceKey, deviceKey)
	}
	if len(result.Ticket) == 0 {
		t.Error("the confirmation carried no ticket")
	}
	// One fetch and then three polls, in that order.
	want := []uint32{
		protocol.QRSubCommandFetch,
		protocol.QRSubCommandPoll,
		protocol.QRSubCommandPoll,
		protocol.QRSubCommandPoll,
	}
	if len(fake.asked) != len(want) {
		t.Fatalf("the machine made %d requests, want %d", len(fake.asked), len(want))
	}
	for i := range want {
		if fake.asked[i] != want[i] {
			t.Errorf("request %d asked for %d, want %d", i, fake.asked[i], want[i])
		}
	}
}

// A code nobody scans has to come back as a lapsed one rather than polling forever.
func TestWaitGivesUpWhenTheCodeLapses(t *testing.T) {
	options := instantOptions()
	options.Lifetime = time.Second
	// A clock that runs a second per reading, so the code's lifetime is over by the time the
	// first poll has been answered.
	readings := 0
	options.Now = func() time.Time {
		readings++
		return time.Unix(1000, 0).Add(time.Duration(readings) * time.Second)
	}

	fake := &fakeExchange{}
	machine, err := NewQRLogin(fake, referenceDevice(), options)
	if err != nil {
		t.Fatalf("NewQRLogin: %v", err)
	}
	fake.key = machine.shareKey
	fake.replies = append(fake.replies, replyFrame(t, machine.shareKey, scanPayload(0x01, 0, nil)))

	if _, err := machine.Wait(context.Background(), QRCode{Signature: []byte{1}}); !errors.Is(err, ErrScanTimeout) {
		t.Errorf("Wait gave %v, want ErrScanTimeout", err)
	}
}

func TestWaitReportsExpiryWhenKeylessReplyArrivesAtDeadline(t *testing.T) {
	payload := append([]byte{0x00}, protocol.U32(0)...)
	payload = append(payload, protocol.U32(1234567890)...)
	payload = append(payload, protocol.U32(0)...)
	payload = append(payload, protocol.WriteTLVs([]protocol.TLVItem{
		{Tag: protocol.QRConfirmTicketTag, Value: []byte{0x01}},
	})...)
	machine, fake := newMachine(t,
		transEmpReply(protocol.QRSubCommandPoll, referenceDevice().AppID, payload))
	readings := 0
	machine.options.Now = func() time.Time {
		readings++
		// Deadline, pre-poll check and packet timestamp are within the lifetime.
		// The fourth read occurs after the reply has arrived.
		if readings < 4 {
			return time.Unix(1000, 0)
		}
		return time.Unix(1000, 0).Add(machine.options.Lifetime)
	}
	if _, err := machine.Wait(context.Background(), QRCode{Signature: []byte{1}}); !errors.Is(err, ErrScanTimeout) {
		t.Fatalf("late keyless reply: %v, want ErrScanTimeout", err)
	}
	if len(fake.asked) != 1 {
		t.Fatalf("expected exactly one poll, got %d", len(fake.asked))
	}
}

// A reply that answers something the machine did not ask is a reply to somebody else's
// request, and taking it as an answer would be worse than failing.
func TestWaitRejectsAReplyToAnotherRequest(t *testing.T) {
	machine, fake := newMachine(t, fetchPayload(t, referenceURL))
	fake.replies = append(fake.replies, replyFrame(t, machine.shareKey,
		transEmpReply(protocol.QRSubCommandFetch, referenceDevice().AppID, []byte{0x00})))

	if _, err := machine.Wait(context.Background(), QRCode{Signature: []byte{1}}); err == nil {
		t.Error("a reply to a fetch was accepted as a poll's answer")
	}
}

// A transport that fails has to surface as a failure, not as a code that never arrives.
func TestTransportFailuresSurface(t *testing.T) {
	machine, fake := newMachine(t)
	fake.err = errors.New("the connection dropped")
	if _, err := machine.Fetch(context.Background()); err == nil {
		t.Error("a failed exchange was accepted")
	}
}

// A confirmation without the device key cannot be used to log in, so it is not a result.
func TestConfirmationWithoutADeviceKeyIsRejected(t *testing.T) {
	payload := append([]byte{0x00}, protocol.U32(0)...)
	payload = append(payload, protocol.U32(1234567890)...)
	payload = append(payload, protocol.U32(0)...)
	payload = append(payload, protocol.WriteTLVs([]protocol.TLVItem{
		{Tag: protocol.QRConfirmTicketTag, Value: []byte{0x01}},
	})...)
	machine, _ := newMachine(t,
		transEmpReply(protocol.QRSubCommandPoll, referenceDevice().AppID, payload))
	if _, err := machine.Wait(context.Background(), QRCode{Signature: []byte{1}}); err == nil {
		t.Error("a confirmation with no device key was accepted")
	}
}

// The machine agrees its own key, so two of them do not share one.
func TestMachinesAgreeSeparateKeys(t *testing.T) {
	first, _ := newMachine(t)
	second, _ := newMachine(t)
	if bytes.Equal(first.shareKey, second.shareKey) {
		t.Error("two machines agreed the same key")
	}
	if len(first.shareKey) != 16 {
		t.Errorf("the agreed key is %d bytes, want 16", len(first.shareKey))
	}
}
