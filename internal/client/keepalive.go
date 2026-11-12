package client

import (
	"crypto/rand"
	"sync"
	"time"

	"openopq/internal/protocol"
)

// The keep-alive's own timing. A capture shows one every ten seconds on a connection that is
// otherwise quiet.
const DefaultKeepAliveInterval = 10 * time.Second

// keepAlive holds a connection open by sending the packet the client sends between requests.
//
// It sends without waiting for the answer. The answer arrives on the same stream as the
// answers to real requests, and the connection routes it aside rather than to a caller, so
// there is nothing here to read — and nothing to do with it if there were, since a
// keep-alive reports only that the connection is still there.
type keepAlive struct {
	conn      Connection
	input     protocol.HeartbeatInput
	interval  time.Duration
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// startKeepAlive begins sending, or reports that it cannot.
//
// A keep-alive is not signed and carries no account, so the only thing that identifies who
// it is from is the install token. Without one there is nothing to send, and a caller that
// has not supplied one simply has no keep-alive rather than a malformed one.
func startKeepAlive(conn Connection, token string, interval time.Duration) (*keepAlive, error) {
	if conn == nil || token == "" {
		return nil, nil
	}
	if interval <= 0 {
		interval = DefaultKeepAliveInterval
	}
	raw := make([]byte, 2)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	k := &keepAlive{
		conn: conn,
		input: protocol.HeartbeatInput{
			ConnectionID:   uint16(raw[0])<<8 | uint16(raw[1]),
			Token:          token,
			ConnectionTime: time.Now().Unix(),
		},
		interval: interval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go k.run()
	return k, nil
}

func (k *keepAlive) run() {
	defer close(k.done)

	// The service is told the client is there, and what the time is, before anything is asked
	// of it. A capture shows both going out on a fresh connection, both of them answered, and
	// the requests that follow coming after — so the warming up happens here rather than on
	// the first tick, which is ten seconds too late.
	if err := k.warmUp(); err != nil {
		return
	}

	ticker := time.NewTicker(k.interval)
	defer ticker.Stop()
	for {
		select {
		case <-k.stop:
			return
		case <-ticker.C:
			// The sequence is one byte wide in the packet and starts over when it runs out,
			// which is what the field can hold.
			k.input.Sequence++
			if err := k.conn.Send(protocol.BuildHeartbeat(k.input)); err != nil {
				// A keep-alive that cannot be sent is a connection that is going. There is
				// no one to tell here — the next request will fail where a caller can see
				// it — so this stops rather than filling the connection with attempts.
				return
			}
		}
	}
}

// warmUp sends the two packets a connection opens with, one after the other, which is the
// order a capture has them in. Their answers are not waited for: neither carries a sequence
// that a caller is holding, and the connection puts them aside on its own.
func (k *keepAlive) warmUp() error {
	for _, build := range []func(protocol.HeartbeatInput) []byte{
		protocol.BuildHeartbeat,
		protocol.BuildCorrectTime,
	} {
		if err := k.conn.Send(build(k.input)); err != nil {
			return err
		}
		k.input.Sequence++
	}
	return nil
}

// Close stops the keep-alive and waits for it to finish, so a caller that has closed a bot
// knows nothing is still writing to it.
func (k *keepAlive) Close() {
	if k == nil {
		return
	}
	select {
	case <-k.done:
		return
	default:
	}
	k.closeOnce.Do(func() { close(k.stop) })
	<-k.done
}
