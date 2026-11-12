package client

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"openopq/internal/protocol"
)

// countingConn records what is sent to it, on a channel so the test can wait for a
// keep-alive rather than guess how long one takes.
type countingConn struct {
	sends chan []byte
}

func (c *countingConn) Send(packet []byte) error {
	c.sends <- append([]byte(nil), packet...)
	return nil
}

func (c *countingConn) Receive(context.Context) (protocol.SSOResponse, error) {
	return protocol.SSOResponse{}, errors.New("nothing is sent to this connection")
}

func (c *countingConn) Exchange(context.Context, []byte, uint32) (protocol.SSOResponse, error) {
	return protocol.SSOResponse{}, errors.New("nothing is sent to this connection")
}

func newCountingConn() *countingConn { return &countingConn{sends: make(chan []byte, 16)} }

// A connection opens with two packets, and they have to be the ones the client sends, which
// the capture settled: the commands they name, and the service number where a length would
// otherwise be.
func TestKeepAliveSendsTheOpeningPair(t *testing.T) {
	conn := newCountingConn()
	keep, err := startKeepAlive(conn, protocol.HeartbeatToken(bytes.Repeat([]byte{0x5A}, 16)), 20*time.Millisecond)
	if err != nil {
		t.Fatalf("startKeepAlive: %v", err)
	}
	if keep == nil {
		t.Fatal("no keep-alive was started")
	}
	defer keep.Close()

	for i, want := range []string{protocol.HeartbeatCommand, protocol.CorrectTimeCommand} {
		select {
		case sent := <-conn.sends:
			if !bytes.Contains(sent, []byte(want)) {
				t.Errorf("the opening packet %d does not name %s", i, want)
			}
			if got := uint32(sent[0])<<24 | uint32(sent[1])<<16 | uint32(sent[2])<<8 | uint32(sent[3]); got != 13 {
				t.Errorf("opening packet %d opens with %d, want the service number 13", i, got)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d opening packets arrived", i)
		}
	}

	// And the connection is held open from there, with the keep-alive alone.
	select {
	case sent := <-conn.sends:
		if !bytes.Contains(sent, []byte(protocol.HeartbeatCommand)) {
			t.Error("what follows the opening pair is not a keep-alive")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection was not held open")
	}
}

// The sequence is the one byte that moves from one keep-alive to the next.
func TestKeepAliveSequenceRises(t *testing.T) {
	conn := newCountingConn()
	keep, err := startKeepAlive(conn, "Ib token", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("startKeepAlive: %v", err)
	}
	defer keep.Close()

	// The two opening packets are different from each other, so they are drained before the
	// comparison; the second of them is not a keep-alive and would move more than the sequence.
	for i := 0; i < 2; i++ {
		select {
		case <-conn.sends:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d opening packets arrived", i)
		}
	}

	var first []byte
	select {
	case first = <-conn.sends:
	case <-time.After(5 * time.Second):
		t.Fatal("no keep-alive arrived")
	}
	select {
	case second := <-conn.sends:
		if second[8] != first[8]+1 {
			t.Errorf("the sequence went from %d to %d", first[8], second[8])
		}
		for i := range second {
			if i == 8 || first[i] == second[i] {
				continue
			}
			t.Errorf("byte %d moved as well: %02x then %02x", i, first[i], second[i])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("only one keep-alive arrived")
	}
}

// Without a token there is nothing to identify the install with, so there is nothing to
// send: a keep-alive without one would be a malformed packet rather than a quiet client.
func TestKeepAliveNeedsAToken(t *testing.T) {
	keep, err := startKeepAlive(newCountingConn(), "", 0)
	if err != nil {
		t.Fatalf("startKeepAlive: %v", err)
	}
	if keep != nil {
		t.Error("a keep-alive was started with nothing to identify it")
	}
}

// Closing has to stop the sending, not leave a goroutine writing to a closed connection.
func TestKeepAliveStopsWhenClosed(t *testing.T) {
	conn := newCountingConn()
	keep, err := startKeepAlive(conn, "Ib token", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("startKeepAlive: %v", err)
	}
	select {
	case <-conn.sends:
	case <-time.After(5 * time.Second):
		t.Fatal("no keep-alive arrived")
	}
	keep.Close()
	// Drain whatever was already in flight, then make sure nothing new arrives.
	for len(conn.sends) > 0 {
		<-conn.sends
	}
	select {
	case sent := <-conn.sends:
		t.Errorf("a keep-alive was sent after closing: %x", sent[:8])
	case <-time.After(200 * time.Millisecond):
	}
}

// Close is safe to call on a bot that never started one, and on one that has been closed.
func TestKeepAliveCloseIsSafe(t *testing.T) {
	var absent *keepAlive
	absent.Close()
	keep, err := startKeepAlive(newCountingConn(), "Ib token", time.Hour)
	if err != nil {
		t.Fatalf("startKeepAlive: %v", err)
	}
	keep.Close()
	keep.Close()
}
