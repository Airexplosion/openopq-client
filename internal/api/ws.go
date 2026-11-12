package api

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"strings"

	"openopq/internal/protocol"
)

// The event websocket reproduces OPQBot's `/ws`: a client connects and receives every incoming
// message as the OPQ event JSON. Implemented against the stdlib (no websocket dependency): the
// upgrade handshake and server→client text framing are both small and fixed.

const wsMagic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// eventSource is the part of the bot the websocket needs: a per-connection subscription to the
// event stream. The concrete client bot provides it; a bot that does not is served a plain error.
type eventSource interface {
	Subscribe() (<-chan protocol.MessageEvent, func())
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	src, ok := s.bot.(eventSource)
	if !ok {
		writeBase(w, RetError, "event stream not supported")
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" || !strings.Contains(strings.ToLower(r.Header.Get("Upgrade")), "websocket") {
		writeBase(w, RetError, "not a websocket upgrade")
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeBase(w, RetError, "connection cannot be upgraded")
		return
	}
	conn, bufrw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()

	handshake := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + wsAccept(key) + "\r\n\r\n"
	if _, err := bufrw.WriteString(handshake); err != nil {
		return
	}
	if err := bufrw.Flush(); err != nil {
		return
	}
	s.logger.Debug("ws client connected", "remote", r.RemoteAddr)

	events, cancel := src.Subscribe()
	defer cancel()

	// A reader goroutine turns a client disconnect (or any client frame error) into a signal to
	// stop writing. We do not interpret client frames; this bot's ws is push-only.
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		buf := make([]byte, 1024)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			payload := opqEventJSON(event)
			if payload == nil {
				continue // an event this adapter does not render
			}
			if err := wsWriteText(bufrw, payload); err != nil {
				return
			}
			if err := bufrw.Flush(); err != nil {
				return
			}
		case <-closed:
			return
		}
	}
}

// wsAccept computes the Sec-WebSocket-Accept value from the client's key.
func wsAccept(key string) string {
	digest := sha1.Sum([]byte(key + wsMagic))
	return base64.StdEncoding.EncodeToString(digest[:])
}

// wsReadText reads one client→server message frame and returns its (unmasked) payload. Ping
// and pong control frames are skipped; a close frame ends the read. Fragmentation is treated as
// a single frame, which is enough for the small action JSONs this takes.
func wsReadText(rw *bufio.ReadWriter) ([]byte, error) {
	for {
		var h [2]byte
		if _, err := io.ReadFull(rw, h[:]); err != nil {
			return nil, err
		}
		opcode := h[0] & 0x0f
		masked := h[1]&0x80 != 0
		length := int(h[1] & 0x7f)
		switch length {
		case 126:
			var ext [2]byte
			if _, err := io.ReadFull(rw, ext[:]); err != nil {
				return nil, err
			}
			length = int(binary.BigEndian.Uint16(ext[:]))
		case 127:
			var ext [8]byte
			if _, err := io.ReadFull(rw, ext[:]); err != nil {
				return nil, err
			}
			length = int(binary.BigEndian.Uint64(ext[:]))
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(rw, mask[:]); err != nil {
				return nil, err
			}
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(rw, payload); err != nil {
			return nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		switch opcode {
		case 0x0, 0x1, 0x2: // continuation / text / binary
			return payload, nil
		case 0x8: // close
			return nil, io.EOF
		default: // ping/pong and others: skip
			continue
		}
	}
}

// wsWriteText writes one unmasked server→client text frame (a whole message, FIN set).
func wsWriteText(w *bufio.ReadWriter, payload []byte) error {
	header := make([]byte, 0, 10)
	header = append(header, 0x81) // FIN + opcode 1 (text)
	switch n := len(payload); {
	case n < 126:
		header = append(header, byte(n))
	case n < 65536:
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(n))
		header = append(header, 126, ext[0], ext[1])
	default:
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		header = append(header, 127)
		header = append(header, ext[:]...)
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}
