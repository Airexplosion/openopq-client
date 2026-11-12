package bot

import (
	"context"
	"encoding/json"
	"fmt"
)

// Stub is a Bot that accepts every call and reports the protocol layer is not attached.
// It exists so the HTTP surface can be run, exercised and tested on its own; wiring it
// to a real client is what turns it into a working framework.
type Stub struct{}

// NewStub builds the placeholder implementation.
func NewStub() *Stub { return &Stub{} }

func (s *Stub) QRCode(context.Context) ([]byte, error) {
	return nil, ErrNotConnected
}

func (s *Stub) LoadSession(context.Context) error { return ErrNotConnected }

func (s *Stub) Self(context.Context) (Self, error) { return Self{}, ErrNotConnected }

func (s *Stub) Groups(context.Context) ([]Group, error) { return nil, ErrNotConnected }

func (s *Stub) Call(_ context.Context, function string, _ json.RawMessage) (any, error) {
	return nil, fmt.Errorf("%w: cannot call %s", ErrNotConnected, function)
}
