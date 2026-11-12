package client

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"openopq/internal/bot"
	"openopq/internal/protocol"
)

// connectedBot builds a bot over a scripted exchange and hands back the exchange, with the
// key the two sides agreed already in place.
//
// The bot makes its own flow, so that key only exists afterwards — which the test can reach
// because it is in the same package, and which is also what a service could do, the
// agreement being with it.
func connectedBot(t *testing.T) (*Bot, *fakeExchange) {
	t.Helper()
	device := referenceDevice()
	fake := &fakeExchange{}
	b := New(BotOptions{
		Exchange: fake,
		Device:   device,
		Session:  Session{AppID: device.AppID, MsfID: 2052, NTSign: "00-abc-01"},
	})
	if _, err := b.session(context.Background()); err != nil {
		t.Fatalf("session: %v", err)
	}
	fake.key = b.login.shareKey
	return b, fake
}

// registerReply is what the service answers an announcement it accepted.
func registerReply() []byte {
	return protocol.Marshal(protocol.StringField(2, "register success"))
}

// afterScanScript is the replies a login takes once a code has been fetched: the scan being
// confirmed, the login, and the announcement a login ends with.
func afterScanScript(t *testing.T, key, deviceKey []byte) [][]byte {
	t.Helper()
	return [][]byte{
		replyFrame(t, key, scanPayload(0x00, scanUin, deviceKey)),
		loginReply(t, key, deviceKey, 0),
		registerReply(),
	}
}

// loginScript is the whole flow, from the code to the announcement.
func loginScript(t *testing.T, key, deviceKey []byte) [][]byte {
	t.Helper()
	return append([][]byte{replyFrame(t, key, fetchPayload(t, referenceURL))},
		afterScanScript(t, key, deviceKey)...)
}

func TestScanCannotUseAnotherAccountsDevice(t *testing.T) {
	b, fake := connectedBot(t)
	b.options.ExpectedUin = scanUin + 1
	deviceKey := bytes.Repeat([]byte{0x5A}, 16)
	fake.replies = append(fake.replies, loginScript(t, fake.key, deviceKey)...)
	if _, err := b.QRCode(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.LoadSession(context.Background()); err == nil || !strings.Contains(err.Error(), "does not match selected account") {
		t.Fatalf("wrong account accepted: %v", err)
	}
	if b.Session().Uin != 0 || b.Credentials().Complete() {
		t.Fatal("mismatched account was installed")
	}
}

func TestBotFetchesAndLogsIn(t *testing.T) {
	b, fake := connectedBot(t)
	deviceKey := bytes.Repeat([]byte{0x5A}, 16)
	fake.replies = append(fake.replies, loginScript(t, fake.key, deviceKey)...)

	image, err := b.QRCode(context.Background())
	if err != nil {
		t.Fatalf("QRCode: %v", err)
	}
	if !bytes.Equal(image, []byte{0x89, 0x50}) {
		t.Errorf("the code came back as %x", image)
	}

	if err := b.LoadSession(context.Background()); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	credentials := b.Credentials()
	if !credentials.Complete() {
		t.Fatalf("the credentials are incomplete: %+v", credentials)
	}

	self, err := b.Self(context.Background())
	if err != nil {
		t.Fatalf("Self: %v", err)
	}
	if self.Uin != scanUin {
		t.Errorf("account = %d, want %d", self.Uin, scanUin)
	}
	if !self.Online {
		t.Error("a signed-in account reports itself offline")
	}
	// The bot settles on the account the scan named, so a caller can persist it.
	if b.Session().Uin != scanUin {
		t.Errorf("the settled session names account %d", b.Session().Uin)
	}
}

func TestBotAsksForACodeBeforeItCanLoad(t *testing.T) {
	b, _ := connectedBot(t)
	if err := b.LoadSession(context.Background()); !errors.Is(err, bot.ErrNotConnected) {
		t.Errorf("LoadSession with no code gave %v, want ErrNotConnected", err)
	}
}

func TestBotReportsNothingBeforeThereIsASession(t *testing.T) {
	b, _ := connectedBot(t)
	if _, err := b.Self(context.Background()); !errors.Is(err, bot.ErrNotConnected) {
		t.Errorf("Self before a login gave %v, want ErrNotConnected", err)
	}
}

// A bot that has not been wired to anything has to say that, rather than report a code it
// never asked for.
func TestBotNeedsAWire(t *testing.T) {
	b := New(BotOptions{Device: referenceDevice()})
	if _, err := b.QRCode(context.Background()); err == nil {
		t.Error("an unwired bot produced a code")
	}
}

// Await is the whole flow in one call, and the code is shown through the callback rather
// than returned, because a caller that shows it has nowhere else to put it.
func TestBotAwaitRunsTheWholeFlow(t *testing.T) {
	b, fake := connectedBot(t)
	deviceKey := bytes.Repeat([]byte{0x5A}, 16)
	fake.replies = append(fake.replies, replyFrame(t, fake.key, fetchPayload(t, referenceURL)))
	fake.replies = append(fake.replies, replyFrame(t, fake.key, scanPayload(0x01, 0, nil)))
	fake.replies = append(fake.replies, afterScanScript(t, fake.key, deviceKey)...)

	shown := 0
	credentials, err := b.Await(context.Background(), func(image []byte) error {
		shown++
		if len(image) == 0 {
			return errors.New("the callback was given nothing to show")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if shown != 1 {
		t.Errorf("the code was shown %d times, want once", shown)
	}
	if !credentials.Complete() {
		t.Errorf("Await returned incomplete credentials: %+v", credentials)
	}
}

// A login is not over when the tickets come back. The account has to be announced, and until
// it is, the session is one the service knows nothing about — which is what decides whether
// the bot reports itself online.
func TestBotAnnouncesTheClient(t *testing.T) {
	b, fake := connectedBot(t)
	deviceKey := bytes.Repeat([]byte{0x5A}, 16)
	fake.replies = append(fake.replies, loginScript(t, fake.key, deviceKey)...)

	if _, err := b.QRCode(context.Background()); err != nil {
		t.Fatalf("QRCode: %v", err)
	}
	if err := b.LoadSession(context.Background()); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}

	if len(fake.calls) != 1 || fake.calls[0] != protocol.RegisterCommand {
		t.Fatalf("the login ended with %v, want one %s", fake.calls, protocol.RegisterCommand)
	}
	// What went out names the device and the service, and leaves the fields nothing here knows
	// to the caller. The fake records every body it was given, and the login's is the one
	// before this.
	sent, err := protocol.EveryField(fake.sent[len(fake.sent)-1])
	if err != nil {
		t.Fatalf("the announcement does not read: %v", err)
	}
	if field, ok := protocol.FirstField(sent, 1); !ok || string(field.Data) == "" {
		t.Error("the announcement names no device")
	}
	if field, ok := protocol.FirstField(sent, 5); !ok || field.Number == 0 {
		t.Error("the announcement names no service")
	}

	// The tickets the login yielded are now what every later request is built from, which is
	// the difference between a packet the service accepts and one it refuses.
	session := b.Session()
	if len(session.Token) == 0 || len(session.D2) == 0 || len(session.SessionKey) == 0 {
		t.Errorf("the session carries no tickets: %+v", session)
	}
	if !bytes.Equal(session.SessionKey, b.Credentials().SessionKey) {
		t.Error("the session key is not the one the login yielded")
	}
	self, err := b.Self(context.Background())
	if err != nil {
		t.Fatalf("Self: %v", err)
	}
	if !self.Online {
		t.Error("a bot whose announcement was accepted reports itself offline")
	}
}

// An announcement the service refused is a login that did not finish, and the bot has to say
// so rather than report an account that nothing will be delivered to.
func TestBotReportsARefusedAnnouncement(t *testing.T) {
	b, fake := connectedBot(t)
	deviceKey := bytes.Repeat([]byte{0x5A}, 16)
	fake.replies = append(fake.replies, loginScript(t, fake.key, deviceKey)...)
	// Replace the acceptance with a refusal.
	fake.replies[len(fake.replies)-1] = protocol.Marshal(
		protocol.StringField(2, "device is locked"))

	if _, err := b.QRCode(context.Background()); err != nil {
		t.Fatalf("QRCode: %v", err)
	}
	err := b.LoadSession(context.Background())
	if err == nil {
		t.Fatal("a refused announcement was accepted")
	}
	if !strings.Contains(err.Error(), "device is locked") {
		t.Errorf("the service's own words were lost: %v", err)
	}
	self, err := b.Self(context.Background())
	if err != nil {
		t.Fatalf("Self: %v", err)
	}
	if self.Online {
		t.Error("a bot whose announcement was refused reports itself online")
	}
}

// A caller that has the login's second credential says so, and the bot uses it rather than
// the fallback.
func TestBotUsesTheSuppliedCredential(t *testing.T) {
	device := referenceDevice()
	fake := &fakeExchange{}
	supplied := bytes.Repeat([]byte{0x33}, 40)
	b := New(BotOptions{
		Exchange:   fake,
		Device:     device,
		Session:    Session{AppID: device.AppID, MsfID: 2052},
		LoginInput: LoginInput{Ticket16A: supplied},
	})
	if _, err := b.session(context.Background()); err != nil {
		t.Fatalf("session: %v", err)
	}
	fake.key = b.login.shareKey
	deviceKey := bytes.Repeat([]byte{0x5A}, 16)
	fake.replies = append(fake.replies, loginScript(t, fake.key, deviceKey)...)

	if _, err := b.QRCode(context.Background()); err != nil {
		t.Fatalf("QRCode: %v", err)
	}
	if err := b.LoadSession(context.Background()); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if !b.Credentials().Complete() {
		t.Error("the login did not complete with a supplied credential")
	}
}

// The session the exchange speaks for has to name the account once there is one, or the
// login would be addressed to nobody.
func TestBotNamesTheAccountOnTheLogin(t *testing.T) {
	b, fake := connectedBot(t)
	deviceKey := bytes.Repeat([]byte{0x5A}, 16)
	fake.replies = append(fake.replies, loginScript(t, fake.key, deviceKey)...)
	if _, err := b.QRCode(context.Background()); err != nil {
		t.Fatalf("QRCode: %v", err)
	}
	if err := b.LoadSession(context.Background()); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if fake.logins != 1 {
		t.Fatalf("the service was asked to log in %d times", fake.logins)
	}
}
