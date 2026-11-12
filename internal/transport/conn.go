// Package transport carries packets to and from a service.
//
// It moves bytes and frames them; it does not know what any of them mean. Keeping that
// separation is what lets the framing be tested against a pipe instead of a network, and
// the packet decoding be tested against bytes that never crossed one.
package transport

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"openopq/internal/protocol"
)

// ErrClosed reports use of a connection that has been closed. It is told apart from a
// network failure because the caller caused it, so retrying is pointless.
var ErrClosed = errors.New("transport: connection is closed")

// ErrNoReply reports that the connection ended while a reply was still waited for.
var ErrNoReply = errors.New("transport: the connection ended before the reply arrived")

// Options are the limits a connection runs under.
//
// A zero Options means the defaults, not "no limits". There is no way to ask for no
// timeout, because a service that stops answering should surface as an error rather than
// pin a caller forever; a caller that wants to wait longer should raise the limit.
type Options struct {
	// DialTimeout bounds the connect.
	DialTimeout time.Duration
	// ReadTimeout bounds one wait, for a reply or for a message nobody asked for.
	ReadTimeout time.Duration
	// WriteTimeout bounds one send.
	WriteTimeout time.Duration
	// KeepAlive is the period of the TCP keepalive probes. A login connection sits idle
	// between packets, and a connection silently dropped by a middlebox has to surface as
	// an error rather than as a hang.
	KeepAlive time.Duration
	// SkipGreeting leaves the service's opening handshake out. A test that supplies its own
	// socket has no service to greet, and a connection that is not greeted is one the service
	// will ignore rather than refuse — so this is for tests, and only for tests.
	SkipGreeting bool
	// Logger, when set, records every frame that crosses the connection, frame by frame, with
	// how much of it was understood. A connection that carries nothing is otherwise
	// indistinguishable from one whose packets are being dropped for a reason, and the
	// reason is only ever visible in the bytes.
	Logger *slog.Logger
}

// DefaultOptions are what a caller gets for the fields it leaves at zero.
func DefaultOptions() Options {
	return Options{
		DialTimeout:  15 * time.Second,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		KeepAlive:    30 * time.Second,
	}
}

// withDefaults fills in the fields the caller left at zero.
func (o Options) withDefaults() Options {
	defaults := DefaultOptions()
	if o.DialTimeout == 0 {
		o.DialTimeout = defaults.DialTimeout
	}
	if o.ReadTimeout == 0 {
		o.ReadTimeout = defaults.ReadTimeout
	}
	if o.WriteTimeout == 0 {
		o.WriteTimeout = defaults.WriteTimeout
	}
	if o.KeepAlive == 0 {
		o.KeepAlive = defaults.KeepAlive
	}
	return o
}

// message is one packet off the wire, with the attempt to decode it.
type message struct {
	payload  []byte
	response protocol.SSOResponse
	decoded  bool
}

// Conn is one connection to a service.
//
// One goroutine reads the connection and everything else hands it work, because the service
// multiplexes: a reply to a request and a message no request asked for arrive interleaved on
// the same stream, and a caller that read the stream itself would take whichever came first.
// So each packet is routed by the sequence number it carries — to the caller waiting for it,
// or aside if nothing is.
type Conn struct {
	conn    net.Conn
	options Options

	sendMu sync.Mutex
	closed atomic.Bool

	// pending holds the callers waiting for a reply, keyed by the sequence they sent.
	pendingMu sync.Mutex
	pending   map[uint32]chan protocol.SSOResponse

	// unsolicited holds the packets nobody is waiting for. It is bounded: a service that
	// sends faster than a caller reads is a service to stop listening to rather than to
	// grow a queue for.
	unsolicited chan message

	// recentPushes remembers the sequences of the last few messages nobody asked for. The
	// service repeats a push when it is unsure the first one arrived, and handing the same
	// one to a caller twice invites it to act on it twice. Bounded, because a connection
	// that runs for a week would otherwise remember every push it ever saw.
	recentMu     sync.Mutex
	recentPushes []uint32

	// done closes when the connection ends, for whatever reason.
	done     chan struct{}
	closeOne sync.Once

	readErrMu sync.Mutex
	readErr   error

	// sessionKey is the login's D2 key, under which the service enciphers every reply to a
	// post-login command. It is empty until a login installs it (SetSessionKey), and the read
	// loop needs it to decipher — and so to match — those replies; without it a reply to
	// register or any later command cannot be read and its caller times out.
	sessionKeyMu sync.Mutex
	sessionKey   []byte

	// logger records frames when the caller asked for a record, and discards them otherwise.
	logger *slog.Logger
}

// Closed includes sockets whose read loop ended because the peer disconnected.
func (c *Conn) Closed() bool {
	if c.closed.Load() {
		return true
	}
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// SetSessionKey installs the login's D2 key, so the read loop can decipher the replies to
// post-login commands. It is called once a login has produced the key and before the first
// command that depends on it (register). Passing it again replaces it, which is what a
// re-login on the same connection needs.
func (c *Conn) SetSessionKey(key []byte) {
	c.sessionKeyMu.Lock()
	c.sessionKey = append([]byte(nil), key...)
	c.sessionKeyMu.Unlock()
}

// currentSessionKey returns the installed D2 key, or nil before a login.
func (c *Conn) currentSessionKey() []byte {
	c.sessionKeyMu.Lock()
	defer c.sessionKeyMu.Unlock()
	return c.sessionKey
}

// Dial opens a connection and opens the service's greeting on it.
//
// The greeting is not a framed packet, so it happens here rather than anywhere above: it has
// to be written and answered before the read loop starts, because a loop that had already
// begun reading would take the answer for a frame.
func Dial(ctx context.Context, address string, options Options) (*Conn, error) {
	options = options.withDefaults()
	dialer := &net.Dialer{Timeout: options.DialTimeout, KeepAlive: options.KeepAlive}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", address, err)
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		// Small packets sent one at a time: Nagle would only add latency.
		_ = tcp.SetNoDelay(true)
		_ = tcp.SetKeepAlive(true)
		if options.KeepAlive > 0 {
			_ = tcp.SetKeepAlivePeriod(options.KeepAlive)
		}
	}
	// DialContext stops watching cancellation after TCP connects. The MSF greeting
	// must also be cancellable so a user disconnect can join an in-flight retry.
	greetingClosed := make(chan struct{})
	stopGreeting := context.AfterFunc(ctx, func() { _ = conn.Close(); close(greetingClosed) })
	greetingErr := greet(conn, options)
	if !stopGreeting() {
		<-greetingClosed
	}
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := greetingErr; err != nil {
		_ = conn.Close()
		return nil, err
	}
	return New(conn, options), nil
}

// greet opens the connection the way the service expects it opened.
//
// A connection that skips this is not refused. It is accepted, read, and never answered, which
// is why the greeting is worth insisting on: without it every later packet looks like a
// service that has nothing to say.
func greet(conn net.Conn, options Options) error {
	if options.SkipGreeting {
		return nil
	}
	if err := conn.SetDeadline(time.Now().Add(options.DialTimeout)); err != nil {
		return fmt.Errorf("set greeting deadline: %w", err)
	}
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	if _, err := conn.Write(protocol.MSFHello()); err != nil {
		return fmt.Errorf("greet the service: %w", err)
	}
	answer := make([]byte, protocol.MSFAckSize)
	if _, err := io.ReadFull(conn, answer); err != nil {
		return fmt.Errorf("read the answer to the greeting: %w", err)
	}
	if err := protocol.CheckMSFAck(answer); err != nil {
		return err
	}
	// Experimental preflight: replay a captured EcdhAccess device handshake (raw, already-framed
	// SSO frames) on the connection before any command, to test whether a server that requires
	// the device-security handshake before trans_emp will then accept it.
	if path := os.Getenv("OPENOPQ_PREFLIGHT"); path != "" {
		blob, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read preflight %s: %w", path, err)
		}
		if _, err := conn.Write(blob); err != nil {
			return fmt.Errorf("send preflight: %w", err)
		}
	}
	return nil
}

// New wraps a connection the caller established. It is what Dial builds on, and it is how a
// test hands in a pipe.
func New(conn net.Conn, options Options) *Conn {
	c := &Conn{
		conn:        conn,
		options:     options.withDefaults(),
		pending:     make(map[uint32]chan protocol.SSOResponse),
		unsolicited: make(chan message, 16),
		done:        make(chan struct{}),
	}
	if options.Logger != nil {
		c.logger = options.Logger
	} else {
		// Resolved once rather than per frame, because the read loop asks for it on every
		// packet and a connection that carries nothing is the common case.
		c.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	go c.readLoop()
	return c
}

// preview is the start of a packet, for a log line about one nothing could be made of.
func preview(payload []byte) string {
	const length = 48
	if len(payload) > length {
		return hex.EncodeToString(payload[:length]) + "…"
	}
	return hex.EncodeToString(payload)
}

// RemoteAddr reports the address of the peer, for logging.
func (c *Conn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

// Send writes one packet.
func (c *Conn) Send(packet []byte) error {
	if c.closed.Load() {
		return ErrClosed
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(c.options.WriteTimeout)); err != nil {
		_ = c.Close()
		return fmt.Errorf("set write deadline: %w", err)
	}
	if err := protocol.WriteFrame(c.conn, packet); err != nil {
		_ = c.Close()
		return fmt.Errorf("send: %w", err)
	}
	if dumpPath := os.Getenv("OPENOPQ_DUMP_TX"); dumpPath != "" {
		if f, err := os.OpenFile(dumpPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			fmt.Fprintf(f, "%x\n", packet)
			_ = f.Close()
		}
	}
	c.logger.Debug("frame sent", "bytes", len(packet), "head", preview(packet))
	return nil
}

// Exchange sends a packet and waits for the reply that answers it.
//
// The sequence is the caller's, because it is the caller that put it in the packet; this
// only has to match the reply against it.
func (c *Conn) Exchange(ctx context.Context, packet []byte, sequence uint32) (protocol.SSOResponse, error) {
	return c.ExchangeObserved(ctx, packet, sequence, nil)
}

// ExchangeObserved reports a completed local write, separately from a server reply.
func (c *Conn) ExchangeObserved(ctx context.Context, packet []byte, sequence uint32, sent func()) (protocol.SSOResponse, error) {
	if c.closed.Load() {
		return protocol.SSOResponse{}, ErrClosed
	}
	waiter := make(chan protocol.SSOResponse, 1)
	c.pendingMu.Lock()
	c.pending[sequence] = waiter
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, sequence)
		c.pendingMu.Unlock()
	}()

	if err := c.Send(packet); err != nil {
		return protocol.SSOResponse{}, err
	}
	if sent != nil {
		sent()
	}

	deadline := time.NewTimer(c.options.ReadTimeout)
	defer deadline.Stop()
	if ctx != nil {
		if caller, ok := ctx.Deadline(); ok {
			if remaining := time.Until(caller); remaining < c.options.ReadTimeout {
				deadline.Reset(remaining)
			}
		}
	}
	select {
	case reply := <-waiter:
		return reply, nil
	case <-deadline.C:
		return protocol.SSOResponse{}, fmt.Errorf("await reply %d: %w", sequence, context.DeadlineExceeded)
	case <-doneOf(ctx):
		return protocol.SSOResponse{}, ctx.Err()
	case <-c.done:
		if err := c.ReadError(); err != nil {
			return protocol.SSOResponse{}, err
		}
		return protocol.SSOResponse{}, ErrClosed
	}
}

// doneOf reports the caller's cancellation, or a channel that never fires when there is
// none.
func doneOf(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}

// ReadError reports why the connection ended, when it ended because of a failure rather than
// a close.
func (c *Conn) ReadError() error {
	c.readErrMu.Lock()
	defer c.readErrMu.Unlock()
	return c.readErr
}

// Receive waits for the next packet nobody asked for and decodes it.
func (c *Conn) Receive(ctx context.Context) (protocol.SSOResponse, error) {
	next, err := c.nextMessage(ctx)
	if err != nil {
		return protocol.SSOResponse{}, err
	}
	if !next.decoded {
		return protocol.SSOResponse{}, fmt.Errorf("decode reply: %w", protocol.ErrShortPacket)
	}
	return next.response, nil
}

// ReceiveRaw waits for the next packet nobody asked for and returns it undecoded, for the
// paths that know the shape of their own reply better than the envelope decoder does.
func (c *Conn) ReceiveRaw(ctx context.Context) ([]byte, error) {
	next, err := c.nextMessage(ctx)
	if err != nil {
		return nil, err
	}
	return next.payload, nil
}

func (c *Conn) nextMessage(ctx context.Context) (message, error) {
	if c.closed.Load() {
		return message{}, ErrClosed
	}
	deadline := time.NewTimer(c.options.ReadTimeout)
	defer deadline.Stop()
	select {
	case next := <-c.unsolicited:
		return next, nil
	case <-deadline.C:
		return message{}, fmt.Errorf("recv: %w", context.DeadlineExceeded)
	case <-doneOf(ctx):
		return message{}, ctx.Err()
	case <-c.done:
		if err := c.ReadError(); err != nil {
			return message{}, err
		}
		return message{}, ErrClosed
	}
}

// readLoop is the one reader. It runs until the connection ends, and hands each packet to
// whoever is waiting for it or puts it aside.
func (c *Conn) readLoop() {
	defer close(c.done)
	for {
		if c.options.ReadTimeout > 0 {
			_ = c.conn.SetReadDeadline(time.Now().Add(c.options.ReadTimeout * 4))
		}
		payload, err := protocol.ReadFrame(c.conn)
		if err != nil {
			c.logger.Debug("the connection ended while reading", "error", err)
			if !c.closed.Load() {
				c.readErrMu.Lock()
				c.readErr = fmt.Errorf("receive: %w", err)
				c.readErrMu.Unlock()
			}
			return
		}
		if dumpPath := os.Getenv("OPENOPQ_DUMP_RX"); dumpPath != "" {
			if f, err := os.OpenFile(dumpPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
				fmt.Fprintf(f, "%x\n", payload)
				_ = f.Close()
			}
		}
		next := message{payload: payload}
		if response, err := protocol.DecodeResponseWithKey(payload, c.currentSessionKey()); err == nil {
			next.response, next.decoded = response, true
		}

		if next.decoded {
			c.pendingMu.Lock()
			waiter, ok := c.pending[next.response.Sequence]
			c.pendingMu.Unlock()
			if ok {
				c.logger.Debug("frame received",
					"bytes", len(payload), "sequence", next.response.Sequence,
					"command", next.response.Command, "code", next.response.Code)
				waiter <- next.response
				continue
			}
		}
		if c.seenPush(next.response.Sequence) {
			continue
		}
		c.logger.Debug("frame received, nobody waiting",
			"bytes", len(payload), "decoded", next.decoded,
			"command", next.response.Command, "code", next.response.Code,
			"sequence", next.response.Sequence, "head", preview(payload))
		select {
		case c.unsolicited <- next:
		default:
			// Nobody is reading, and the service keeps sending. Dropping is better than
			// growing: a queue that never drains is a memory leak with extra steps.
		}
	}
}

// recentPushWindow is how many push sequences are remembered. A repeat of a push arrives
// within moments of the original, so a window this small is generous.
const recentPushWindow = 64

// seenPush reports whether this push has already been handed over, and remembers it if not.
func (c *Conn) seenPush(sequence uint32) bool {
	if sequence == 0 {
		// A push that carries no sequence cannot be told from another, so it is passed on
		// rather than risk dropping one that mattered.
		return false
	}
	c.recentMu.Lock()
	defer c.recentMu.Unlock()
	for _, seen := range c.recentPushes {
		if seen == sequence {
			return true
		}
	}
	c.recentPushes = append(c.recentPushes, sequence)
	if len(c.recentPushes) > recentPushWindow {
		c.recentPushes = c.recentPushes[1:]
	}
	return false
}

// Close shuts the connection down. Closing twice is not an error, so a deferred Close and
// an explicit one can coexist.
func (c *Conn) Close() error {
	closeOne := false
	c.closeOne.Do(func() {
		closeOne = true
		c.closed.Store(true)
	})
	if !closeOne {
		return nil
	}
	// net.Conn permits Close concurrently with Write. Do not wait for sendMu:
	// a blocked writer (or Send's error path) needs Close to unblock it.
	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}
