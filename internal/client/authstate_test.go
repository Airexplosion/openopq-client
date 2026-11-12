package client

import (
	"context"
	"openopq/internal/protocol"
	"testing"
)

func TestServerAuthRefusalInvalidatesOnlineState(t *testing.T) {
	s := session()
	s.SessionKey = []byte("0123456789abcdef")
	conn := &fakeConn{replies: []protocol.SSOResponse{{Code: -10003, Message: "identity rejected"}}}
	e := NewServiceExchange(conn, &fakeSigner{}, s)
	b := &Bot{exchange: e, account: *s, credentials: protocol.Credentials{SessionKey: s.SessionKey}, online: true}
	if _, err := e.Exchange(context.Background(), "trpc.o3.ecdh_access.EcdhAccess.SsoSecureA2Access", nil); err == nil {
		t.Fatal("refusal hidden")
	}
	self, err := b.Self(context.Background())
	if err != nil || self.Online || b.AuthenticationError() == nil {
		t.Fatalf("invalid login still online: %+v %v", self, err)
	}
	e.ResetAuthenticationError()
	if e.AuthenticationError() != nil {
		t.Fatal("fresh login cannot clear the prior authentication failure")
	}
}

func TestPreLoginOrOtherRefusalsDoNotPoisonAuthState(t *testing.T) {
	for _, tc := range []struct {
		code     int32
		loggedIn bool
	}{{-10003, false}, {-1, true}} {
		s := session()
		s.SessionKey = nil
		if tc.loggedIn {
			s.SessionKey = []byte("0123456789abcdef")
		}
		e := NewServiceExchange(&fakeConn{replies: []protocol.SSOResponse{{Code: tc.code}}}, &fakeSigner{}, s)
		_, _ = e.Exchange(context.Background(), protocol.TransEmpCommand, nil)
		if e.AuthenticationError() != nil {
			t.Fatalf("unrelated refusal marked authentication invalid: %+v", tc)
		}
	}
}
