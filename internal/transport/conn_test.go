package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"openopq/internal/protocol"
)

type observedWriter struct {
	net.Conn
	entered chan struct{}
	fail    bool
}

func (c *observedWriter) Write(p []byte) (int, error) {
	close(c.entered)
	if c.fail {
		return 0, io.ErrClosedPipe
	}
	return c.Conn.Write(p)
}

func TestCloseInterruptsBlockedWriter(t *testing.T) {
	near, far := net.Pipe()
	defer far.Close()
	wrapped := &observedWriter{Conn: near, entered: make(chan struct{})}
	c := New(wrapped, Options{WriteTimeout: time.Minute})
	defer c.Close()
	wrote := make(chan error, 1)
	go func() { wrote <- c.Send([]byte("blocked")) }()
	<-wrapped.entered
	closed := make(chan struct{})
	go func() { _ = c.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close blocked on writer mutex")
	}
	select {
	case err := <-wrote:
		if err == nil {
			t.Fatal("blocked write succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("writer not interrupted")
	}
}

func TestWriteFailureClosesTransportAndWakesReader(t *testing.T) {
	near, far := net.Pipe()
	defer far.Close()
	c := New(&observedWriter{Conn: near, entered: make(chan struct{}), fail: true}, Options{})
	defer c.Close()
	if err := c.Send([]byte("fail")); err == nil {
		t.Fatal("missing write error")
	}
	if !c.Closed() {
		t.Fatal("failed transport remained open")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := c.Receive(ctx); errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("receive not woken after write failure")
	}
}

func TestDialCancellationInterruptsGreeting(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	hello := make(chan net.Conn, 1)
	go func() {
		c, err := listener.Accept()
		if err != nil {
			return
		}
		buffer := make([]byte, len(protocol.MSFHello()))
		_, _ = io.ReadFull(c, buffer)
		hello <- c
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		c, err := Dial(ctx, listener.Addr().String(), Options{DialTimeout: time.Minute})
		if c != nil {
			_ = c.Close()
		}
		done <- err
	}()
	select {
	case server := <-hello:
		defer server.Close()
	case <-time.After(time.Second):
		t.Fatal("no greeting")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("greeting ignored cancellation")
	}
}

func TestExchangeObserverSeparatesLocalWriteFromServerReply(t *testing.T) {
	client, far := pair(t, Options{ReadTimeout: time.Second})
	sent := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := client.ExchangeObserved(context.Background(), []byte("heartbeat"), 7, func() { close(sent) })
		done <- err
	}()
	if _, err := protocol.ReadFrame(far); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("completed write was not observed")
	}
	select {
	case err := <-done:
		t.Fatalf("no QQ reply was sent yet: %v", err)
	default:
	}
	go func() { _, _ = far.Write(syntheticReplySeq(7, 0, "", "heartbeat")) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server reply not matched")
	}
}

func TestExchangeObserverDoesNotClaimFailedWriteWasSent(t *testing.T) {
	near, far := net.Pipe()
	defer far.Close()
	client := New(&observedWriter{Conn: near, entered: make(chan struct{}), fail: true}, Options{})
	defer client.Close()
	sent := false
	if _, err := client.ExchangeObserved(context.Background(), []byte("fail"), 1, func() { sent = true }); err == nil {
		t.Fatal("missing failure")
	}
	if sent {
		t.Fatal("failed write logged as sent")
	}
}

// pair returns a connection under test and the far end of a pipe, so the framing can be
// exercised without a network.
func pair(t *testing.T, options Options) (*Conn, net.Conn) {
	t.Helper()
	near, far := net.Pipe()
	client := New(near, options)
	t.Cleanup(func() {
		_ = client.Close()
		_ = far.Close()
	})
	return client, far
}

// syntheticReply builds a reply with the shape a captured one has: a header behind a
// length, and a body behind another. The field meanings are the envelope's; what matters
// here is only that the framing matches.
func syntheticReply(code int32, message, command string) []byte {
	return syntheticReplySeq(1, code, message, command)
}

// drain makes the far end consume whatever the client writes. A pipe is synchronous, so a
// send blocks until the other side reads: a far end that only writes would deadlock the
// caller rather than the connection.
func drain(far net.Conn) {
	go func() {
		buffer := make([]byte, 4096)
		for {
			if _, err := far.Read(buffer); err != nil {
				return
			}
		}
	}()
}

// syntheticReplySeq is the same, with the sequence the reply answers spelled out — which is
// how a caller tells one reply from another.
func syntheticReplySeq(sequence uint32, code int32, message, command string) []byte {
	header := append(protocol.U32(sequence), protocol.U32(uint32(code))...)
	header = append(header, protocol.LV32([]byte(message))...)
	header = append(header, protocol.LV32([]byte(command))...)
	header = append(header, protocol.LV32(nil)...)
	header = append(header, protocol.U32(0)...)
	header = append(header, protocol.LV32([]byte{0xA8, 0x01, 0x00})...)
	header = append(header, protocol.U32(1791716483)...)
	body := protocol.LV32([]byte{0, 0, 0, 4})
	return protocol.LV32(append(protocol.LV32(header), body...))
}

func TestSendFramesThePacket(t *testing.T) {
	client, far := pair(t, Options{})
	packet := []byte("a packet's worth of bytes")
	go func() { _ = client.Send(packet) }()

	got, err := protocol.ReadFrame(far)
	if err != nil {
		t.Fatalf("the peer could not read the frame: %v", err)
	}
	if !bytes.Equal(got, packet) {
		t.Errorf("peer read %q, want %q", got, packet)
	}
}

func TestReceiveDecodesAReply(t *testing.T) {
	client, far := pair(t, Options{})
	go func() { _, _ = far.Write(syntheticReply(0, "", "wtlogin.login")) }()

	response, err := client.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if response.Command != "wtlogin.login" {
		t.Errorf("command = %q, want wtlogin.login", response.Command)
	}
	if !response.Accepted() {
		t.Errorf("a zero code came back as a refusal: %+v", response)
	}
}

func TestReceivePassesARefusalOn(t *testing.T) {
	client, far := pair(t, Options{})
	go func() { _, _ = far.Write(syntheticReply(-10003, "请你重新登录", "wtlogin.login")) }()

	response, err := client.Receive(context.Background())
	if err != nil {
		t.Fatalf("a refusal must arrive as a reply, not an error: %v", err)
	}
	if response.Accepted() {
		t.Error("a refusal reports itself accepted")
	}
	if response.Message != "请你重新登录" {
		t.Errorf("message = %q", response.Message)
	}
}

// The paths that know their own reply shape better than the envelope decoder does need the
// bytes, and a packet that is not an envelope must still be readable.
func TestReceiveRawSkipsTheEnvelope(t *testing.T) {
	client, far := pair(t, Options{})
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	go func() { _ = protocol.WriteFrame(far, payload) }()

	got, err := client.ReceiveRaw(context.Background())
	if err != nil {
		t.Fatalf("ReceiveRaw: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload = %x, want %x", got, payload)
	}
}

func TestReceiveReportsAnUndecodableReply(t *testing.T) {
	client, far := pair(t, Options{})
	go func() { _ = protocol.WriteFrame(far, []byte{0xDE, 0xAD, 0xBE, 0xEF}) }()

	if _, err := client.Receive(context.Background()); err == nil {
		t.Error("a packet that is not a reply was accepted as one")
	}
}

// A server that stops answering must not pin the caller. The failure has to be a timeout,
// not a closed connection, so that a caller can tell the two apart.
func TestReceiveTimesOut(t *testing.T) {
	client, _ := pair(t, Options{ReadTimeout: 60 * time.Millisecond})
	started := time.Now()
	_, err := client.Receive(context.Background())
	if err == nil {
		t.Fatal("Receive returned with nothing to read")
	}
	if errors.Is(err, ErrClosed) {
		t.Errorf("a timeout was reported as a closed connection: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("Receive took %s to give up on a 60ms limit", elapsed)
	}
}

// The caller's deadline wins when it is sooner than the connection's own limit.
func TestReceiveHonoursTheCallerDeadline(t *testing.T) {
	client, _ := pair(t, Options{ReadTimeout: time.Minute})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err := client.Receive(ctx); err == nil {
		t.Error("Receive ignored the caller's deadline")
	}
}

// The length arrives before the body, so a frame that claims more than the limit allows
// has to be refused rather than allocated for.
func TestReceiveRejectsAnOversizedFrame(t *testing.T) {
	client, far := pair(t, Options{})
	go func() { _, _ = far.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF}) }()

	_, err := client.ReceiveRaw(context.Background())
	if err == nil {
		t.Fatal("an oversized frame was accepted")
	}
	if errors.Is(err, ErrClosed) {
		t.Errorf("an oversized frame was reported as a closed connection: %v", err)
	}
}

func TestUseAfterClose(t *testing.T) {
	client, _ := pair(t, Options{})
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := client.Send([]byte("packet")); !errors.Is(err, ErrClosed) {
		t.Errorf("Send after Close gave %v, want ErrClosed", err)
	}
	if _, err := client.Receive(context.Background()); !errors.Is(err, ErrClosed) {
		t.Errorf("Receive after Close gave %v, want ErrClosed", err)
	}
	if _, err := client.ReceiveRaw(context.Background()); !errors.Is(err, ErrClosed) {
		t.Errorf("ReceiveRaw after Close gave %v, want ErrClosed", err)
	}
}

// A deferred Close and an explicit one must be able to coexist, which is what happens when
// a caller closes on a path out of a deferred one.
func TestCloseIsIdempotent(t *testing.T) {
	client, _ := pair(t, Options{})
	for i := 0; i < 3; i++ {
		if err := client.Close(); err != nil {
			t.Fatalf("Close %d: %v", i, err)
		}
	}
}

// A read that fails because the connection was closed under it is the caller's own doing,
// and reporting it as a network fault would send the caller off to reconnect.
func TestCloseInterruptsAWaitingReceive(t *testing.T) {
	client, _ := pair(t, Options{ReadTimeout: time.Minute})
	done := make(chan error, 1)
	go func() {
		_, err := client.Receive(context.Background())
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Errorf("a receive interrupted by Close gave %v, want ErrClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not interrupt the waiting receive")
	}
}

func TestDialToAClosedPortFails(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	options := DefaultOptions()
	options.DialTimeout = 2 * time.Second
	conn, err := Dial(context.Background(), address, options)
	if err == nil {
		_ = conn.Close()
		t.Fatal("dial succeeded against a closed port")
	}
}

// An empty payload is a legal frame, so a service that answers with one must not be read
// as a transport failure.
func TestReceiveRawAcceptsAnEmptyFrame(t *testing.T) {
	client, far := pair(t, Options{})
	go func() { _, _ = far.Write([]byte{0, 0, 0, 4}) }()

	payload, err := client.ReceiveRaw(context.Background())
	if err != nil {
		t.Fatalf("ReceiveRaw: %v", err)
	}
	if len(payload) != 0 {
		t.Errorf("payload = %x, want nothing", payload)
	}
}

// Frames arrive back to back, and reading one must leave the next for the following call.
//
// The two carry different sequences, because two frames that carried the same one would be a
// repeat to the deduplicator rather than two messages, and this is not the test for that.
func TestReceiveLeavesTheNextFrame(t *testing.T) {
	client, far := pair(t, Options{})
	go func() {
		for i, command := range []string{"first", "second"} {
			_, _ = far.Write(syntheticReplySeq(uint32(i+1), 0, "", command))
		}
	}()
	for _, want := range []string{"first", "second"} {
		response, err := client.Receive(context.Background())
		if err != nil {
			t.Fatalf("Receive: %v", err)
		}
		if response.Command != want {
			t.Errorf("command = %q, want %q", response.Command, want)
		}
	}
}

// A half-read frame is a broken stream, not a short packet to be retried: the caller has no
// way to know where the next one begins.
func TestReceiveRejectsATruncatedFrame(t *testing.T) {
	client, far := pair(t, Options{})
	go func() {
		frame := syntheticReply(0, "", "wtlogin.login")
		_, _ = far.Write(frame[:len(frame)-4])
		_ = far.Close()
	}()
	if _, err := client.Receive(context.Background()); err == nil {
		t.Fatal("a truncated frame was accepted")
	}
}

// A reply has to reach the caller that asked for it, even when answers arrive out of order
// — which is what the sequence is for.
func TestExchangeMatchesRepliesBySequence(t *testing.T) {
	client, far := pair(t, Options{})
	// The far end waits until both requests have arrived before answering, because a request
	// only reaches it once its caller has registered — which is what makes the ordering
	// below the thing being tested rather than an accident of who ran first.
	go func() {
		buffer := make([]byte, 4096)
		for reads := 0; reads < 2; {
			if _, err := far.Read(buffer); err != nil {
				return
			}
			reads++
		}
		// Answer the second request first.
		_, _ = far.Write(syntheticReplySeq(2, 0, "", "second"))
		_, _ = far.Write(syntheticReplySeq(1, 0, "", "first"))
	}()

	type answer struct {
		command string
		err     error
	}
	second := make(chan answer, 1)
	first := make(chan answer, 1)
	go func() {
		reply, err := client.Exchange(context.Background(), []byte("b"), 2)
		second <- answer{reply.Command, err}
	}()
	time.Sleep(20 * time.Millisecond)
	go func() {
		reply, err := client.Exchange(context.Background(), []byte("a"), 1)
		first <- answer{reply.Command, err}
	}()

	for _, check := range []struct {
		name string
		got  chan answer
		want string
	}{
		{"the first request", first, "first"},
		{"the second request", second, "second"},
	} {
		select {
		case got := <-check.got:
			if got.err != nil {
				t.Errorf("%s: %v", check.name, got.err)
				continue
			}
			if got.command != check.want {
				t.Errorf("%s got %q, want %q", check.name, got.command, check.want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never got an answer", check.name)
		}
	}
}

// A message no request asked for must not be handed to a caller waiting for one.
func TestExchangeLeavesUnsolicitedMessagesAlone(t *testing.T) {
	client, far := pair(t, Options{})
	drain(far)
	go func() {
		_, _ = far.Write(syntheticReplySeq(99, 0, "", "pushed"))
		_, _ = far.Write(syntheticReplySeq(7, 0, "", "asked"))
	}()

	reply, err := client.Exchange(context.Background(), []byte("a"), 7)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if reply.Command != "asked" {
		t.Errorf("the request was answered with %q", reply.Command)
	}
	// The push that arrived first is still there for whoever wants it.
	pushed, err := client.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if pushed.Command != "pushed" {
		t.Errorf("the push came back as %q", pushed.Command)
	}
}

// A caller waiting for a reply must not be pinned when it never comes.
func TestExchangeGivesUp(t *testing.T) {
	client, far := pair(t, Options{ReadTimeout: 60 * time.Millisecond})
	drain(far)
	started := time.Now()
	if _, err := client.Exchange(context.Background(), []byte("a"), 1); err == nil {
		t.Fatal("Exchange returned with no reply to return")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("Exchange took %s to give up on a 60ms limit", elapsed)
	}
}

// Closing under a waiting caller has to wake it with something it can tell apart from a
// network fault.
func TestExchangeUnblocksOnClose(t *testing.T) {
	client, far := pair(t, Options{ReadTimeout: time.Minute})
	drain(far)
	done := make(chan error, 1)
	go func() {
		_, err := client.Exchange(context.Background(), []byte("a"), 1)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a closed connection answered a request")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not wake the waiting caller")
	}
}

// The service repeats a push when it is unsure the first one arrived, and a caller that acted
// on it twice would act twice. The repeat is dropped; a different push is not.
func TestReceiveDropsARepeatedPush(t *testing.T) {
	client, far := pair(t, Options{})
	drain(far)
	go func() {
		_, _ = far.Write(syntheticReplySeq(11, 0, "", "first"))
		_, _ = far.Write(syntheticReplySeq(11, 0, "", "first"))
		_, _ = far.Write(syntheticReplySeq(12, 0, "", "second"))
	}()

	first, err := client.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if first.Sequence != 11 {
		t.Errorf("the first push carries sequence %d", first.Sequence)
	}
	second, err := client.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	// The repeat is gone, so the next thing to arrive is the push that followed it.
	if second.Sequence != 12 {
		t.Errorf("the repeat was handed over: sequence %d", second.Sequence)
	}
}
