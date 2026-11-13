package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"openopq/internal/bot"
	"openopq/internal/loginstate"
	"openopq/internal/protocol"
)

// Dialer opens a connection. The bot makes one when it needs one and holds it for the life
// of the session, because the sequence numbers a service correlates by are per connection
// and a second connection would start them again.
type Dialer func(ctx context.Context) (Connection, error)

// BotOptions are what a bot needs to start.
type BotOptions struct {
	// MediaCacheMiB limits each account's disk cache; zero uses 256 MiB.
	MediaCacheMiB int64
	LoginStore    *loginstate.Store
	LoginIdentity string
	Reconnect     ReconnectOptions
	Logger        *slog.Logger
	// DisableO3Relay is an explicit research control; account notifications and
	// the login key exchange still run. Normal sessions keep the relay enabled.
	DisableO3Relay bool
	// ExpectedUin prevents a scan by another account from using this account's device.
	ExpectedUin uint32
	// Dial opens the connection a session runs on.
	Dial Dialer
	// Signer signs every packet.
	Signer Signer
	// Exchange, when set, is used instead of building one from Dial and Signer. It is how a
	// caller that already has a way onto the wire — or one that wants to stand in for it —
	// supplies it.
	Exchange Exchange
	// Device describes the client the frames speak for.
	Device protocol.QRDevice
	// Session describes the account, which is empty until one is signed in.
	Session Session
	// Register is what the announcement claims about the client, which is sent as the last
	// step of a login. The two fields the bot knows for itself — the device's GUID and the
	// service the session belongs to — are filled in from the rest of this; what is left is
	// what a caller has to say. There is no capture of this packet anywhere, so a value here
	// that the service cares about is a value that has not been checked.
	Register protocol.RegisterInput
	// Token identifies the installation, which a keep-alive reports. Without one no
	// keep-alive is sent, because there would be nothing to identify it with.
	Token string
	// KeepAlive is how often one is sent. Zero uses the interval a capture shows.
	KeepAlive time.Duration
	// Heartbeat is how often the protocol reports the session is still there. Zero uses the
	// interval the reference waits.
	Heartbeat time.Duration
	// LoginInput carries the parts of a login that the scan does not produce. An empty
	// Ticket16A falls back to the confirmation's own block, which is the only candidate the
	// flow offers and is not confirmed to be the right one.
	LoginInput LoginInput
}

// Bot drives the QR login flow behind the framework's interface.
//
// It holds one connection and one agreed key for the whole session, which is what the flow
// does: the agreement outlives the code it was made for, and the login that follows uses it
// again.
type Bot struct {
	options BotOptions

	mu             sync.Mutex
	closeMu        sync.Mutex
	recoveryCancel context.CancelFunc
	recoveryDone   chan struct{}
	recovery       bot.ReconnectStatus
	terminalErr    error
	lastHeartbeat  bot.HeartbeatStatus
	account        Session
	connection     Connection
	exchange       Exchange
	login          *QRLogin
	keepAlive      *keepAlive
	beat           *ssoHeartbeat
	o3Cancel       context.CancelFunc
	o3Done         chan struct{}
	online         bool
	pending        QRCode
	credentials    protocol.Credentials
	nickname       string
	loginProgress  func(string)
	accountGuard   func(uint32) error
	// subs are the /ws listeners the event loop broadcasts incoming messages to.
	subs subscriberSet
	// msgSeq is the client message sequence outgoing messages carry (separate from the SSO
	// sequence). Seeded once, incremented per send.
	msgSeq uint32
	// contacts maps a uin to what received messages have taught about that account — the modern
	// uid the NT server binds an @mention on, and the nickname to display it with — so an
	// outgoing @mention addressed only by uin can carry both.
	contacts        map[uint32]contact
	profileFailures map[uint32]time.Time
	// byUID is the reverse: the uin last seen for a uid, so a notice (which names people by
	// uid) can report the uins the OPQ/OneBot events use.
	byUID map[string]uint32
	// highway is the cached rich-media upload session (servers + ticket), fetched once.
	highway    protocol.HighwaySession
	obRequests requestRegistry
	obMedia    mediaRegistry
	tempRoutes temporaryRoutes
}

// contact is what the bot has learned about one account from its messages.
type contact struct {
	uid  string
	nick string
}

// remember records a uin→(uid, nick) pairing learned from a received message. A field is only
// overwritten when the new message carries a non-empty value for it.
func (b *Bot) remember(uin uint32, uid, nick string) {
	if uin == 0 {
		return
	}
	b.mu.Lock()
	if b.contacts == nil {
		b.contacts = make(map[uint32]contact)
	}
	if b.byUID == nil {
		b.byUID = make(map[string]uint32)
	}
	c := b.contacts[uin]
	if uid != "" {
		c.uid = uid
		b.byUID[uid] = uin
	}
	if nick != "" {
		c.nick = nick
	}
	b.contacts[uin] = c
	b.mu.Unlock()
}

// uinFor returns the uin last seen for a uid, or zero if none is known.
func (b *Bot) uinFor(uid string) uint32 {
	if uid == "" {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.byUID[uid]
}

// resolveNotice fills a notice's uin fields from the uids it carries, using what received
// messages have taught. A uin stays zero for anyone the bot has not seen speak.
func (b *Bot) resolveNotice(n *protocol.Notice) {
	if n == nil {
		return
	}
	if n.UserUin == 0 {
		n.UserUin = b.uinFor(n.UserUid)
	}
	if n.OperatorUin == 0 {
		n.OperatorUin = b.uinFor(n.OperatorUid)
	}
}

// uidFor returns the uid last seen for a uin, or empty if none is known.
func (b *Bot) uidFor(uin uint32) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.contacts[uin].uid
}

// nickFor returns the nickname last seen for a uin, or empty if none is known.
func (b *Bot) nickFor(uin uint32) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.contacts[uin].nick
}

// New builds a bot. It does not connect: a connection is made when the first call needs one,
// and a failure then is reported to that call rather than to startup.
func New(options BotOptions) *Bot {
	b := &Bot{options: options, account: options.Session}
	// Every request's header reports the device, and it has to be the GUID the device blocks
	// report too. Taking it from the one place the device is described is what keeps a client
	// from claiming to be two machines, which is a state the service notices.
	if b.account.Device == "" {
		b.account.Device = hex.EncodeToString(options.Device.Guid)
	}
	if b.options.LoginIdentity == "" {
		endpoint := ""
		if signer, ok := options.Signer.(interface{ Endpoint() string }); ok {
			endpoint = signer.Endpoint()
		}
		identity, _ := json.Marshal(struct {
			Device   protocol.QRDevice
			Register protocol.RegisterInput
			AppID    uint32
			MsfID    uint32
			NTSign   string
			Framing  protocol.Framing
			Signer   string
		}{options.Device, options.Register, b.account.AppID, b.account.MsfID, b.account.NTSign, b.account.Framing, endpoint})
		digest := sha256.Sum256(identity)
		b.options.LoginIdentity = hex.EncodeToString(digest[:])
	}
	return b
}

// SetLoginStore is called by the application factory before the Bot is used.
func (b *Bot) SetLoginStore(store *loginstate.Store) {
	b.mu.Lock()
	b.options.LoginStore = store
	b.mu.Unlock()
}

// QRCode fetches a code and returns it as a PNG.
//
// The code it returns is remembered, because the call that follows has to poll the same one:
// a second code would be a second scan.
func (b *Bot) QRCode(ctx context.Context) ([]byte, error) {
	login, err := b.session(ctx)
	if err != nil {
		return nil, err
	}
	code, err := login.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	if len(code.Image) == 0 {
		return nil, fmt.Errorf("the service sent a code with no image")
	}

	b.mu.Lock()
	b.pending = code
	b.mu.Unlock()
	return code.Image, nil
}

// LoadSession waits for the code to be scanned, logs the account in, and announces the client.
//
// The announcement is the last step and not an optional one: a login produces tickets, and an
// account the service has not been told about is one nothing is delivered to. What this
// leaves behind is a session that reports itself online, which is what a caller means by
// having loaded one.
func (b *Bot) LoadSession(ctx context.Context) error {
	// Credential-borrow path: bring the bot online with tickets minted by a helper client's
	// native login (A2/D2/SessionKey), skipping the QR fetch/scan/wtlogin.login entirely. The
	// one step that is hostile to o3 delegation (wtlogin.login's signature binding) is thereby
	// avoided; the connection still does o3 (via session()) so register is accepted.
	if credPath := strings.TrimSpace(os.Getenv("OPENOPQ_CREDS")); credPath != "" {
		return b.credentialLogin(ctx, credPath)
	}
	b.mu.Lock()
	login, code := b.login, b.pending
	b.mu.Unlock()
	if login == nil || len(code.Signature) == 0 {
		return fmt.Errorf("%w: no code is waiting to be scanned", bot.ErrNotConnected)
	}

	scan, err := login.Wait(ctx, code)
	if err != nil {
		return err
	}
	if b.options.ExpectedUin != 0 && scan.Uin != b.options.ExpectedUin {
		return fmt.Errorf("scanned account %d does not match selected account %d", scan.Uin, b.options.ExpectedUin)
	}
	b.mu.Lock()
	guard := b.accountGuard
	b.mu.Unlock()
	if guard != nil {
		if err := guard(scan.Uin); err != nil {
			return err
		}
	}
	b.reportLoginProgress("authenticating")
	input := b.options.LoginInput
	if len(input.Ticket16A) == 0 {
		// The confirmation's own block, which is the only part of a scan that could be the
		// login's second credential and is not confirmed to be. It is a default rather than
		// a decision: a caller that knows what belongs there can say so.
		input.Ticket16A = scan.NoPicSig
	}
	// The account is known now, and the login has to be addressed to it.
	b.mu.Lock()
	b.account.Uin = scan.Uin
	b.mu.Unlock()
	if account, ok := b.options.Signer.(interface {
		Account(context.Context, uint32, bool) error
	}); ok {
		if err := account.Account(ctx, scan.Uin, false); err != nil {
			return err
		}
		if _, token := signerSession(b.options.Signer); token != "" {
			b.o3Delegate(ctx, b.exchange, 8*time.Second, false)
		}
	}

	credentials, err := login.Login(ctx, scan, input)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.credentials = credentials
	b.nickname = credentials.NickName
	// The login's tickets are what every request after it is authenticated and enciphered
	// with. The session the exchange holds is the one place they reach the packets from, and
	// it is held by pointer for exactly this reason.
	b.account.Token = credentials.Token010A
	b.account.D2 = credentials.Token0143
	b.account.SessionKey = credentials.SessionKey
	// The modern UID the login yielded; every post-login request carries it in the SSO
	// extension (field 16) so the trpc services can bind the packet to the account.
	b.account.UID = credentials.Uid
	connection := b.connection
	uin := b.account.Uin
	b.mu.Unlock()

	// A fresh login's tickets can be saved (env-gated) in the same JSON shape credentialLogin
	// reads, so a restart can resume via OPENOPQ_CREDS instead of another scan.
	if path := strings.TrimSpace(os.Getenv("OPENOPQ_DUMP_CREDS")); path != "" {
		dumpCreds(path, uin, credentials)
	}

	// Hand the session key to the connection so its read loop can decipher the replies to
	// post-login commands. Register is the first of them; without the key its reply cannot be
	// read and the request times out as if unanswered.
	if setter, ok := connection.(interface{ SetSessionKey([]byte) }); ok {
		setter.SetSessionKey(credentials.SessionKey)
	}

	return b.register(ctx)
}

// dumpCreds writes a login's tickets as the JSON credentialLogin reads, so a restart can resume
// the session without another scan.
func dumpCreds(path string, uin uint32, c protocol.Credentials) {
	payload := struct {
		Uin        uint32 `json:"uin"`
		Token010A  string `json:"token010a"`
		D2         string `json:"d2"`
		SessionKey string `json:"sessionkey"`
		Uid        string `json:"uid"`
	}{uin, hex.EncodeToString(c.Token010A), hex.EncodeToString(c.Token0143),
		hex.EncodeToString(c.SessionKey), c.Uid}
	if raw, err := json.Marshal(payload); err == nil {
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "[dumpcreds] %v\n", err)
		}
	}
}

// credentialLogin brings the bot online from tickets captured elsewhere (a helper client's
// native 7.0.2 login), read as JSON from the named file. It establishes the connection and o3
// state through session(), installs the tickets on the account, and registers.
func (b *Bot) credentialLogin(ctx context.Context, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read creds %s: %w", path, err)
	}
	var c struct {
		Uin        uint32 `json:"uin"`
		Token010A  string `json:"token010a"`
		D2         string `json:"d2"`
		SessionKey string `json:"sessionkey"`
		Uid        string `json:"uid"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return fmt.Errorf("parse creds: %w", err)
	}
	if b.options.ExpectedUin != 0 && c.Uin != b.options.ExpectedUin {
		return fmt.Errorf("credential account does not match selected account %d", b.options.ExpectedUin)
	}
	tok, err1 := hex.DecodeString(strings.TrimSpace(c.Token010A))
	d2, err2 := hex.DecodeString(strings.TrimSpace(c.D2))
	sk, err3 := hex.DecodeString(strings.TrimSpace(c.SessionKey))
	if err1 != nil || err2 != nil || err3 != nil || c.Uin == 0 || len(tok) == 0 || len(d2) == 0 || len(sk) == 0 {
		return fmt.Errorf("creds incomplete: uin=%d a2=%d d2=%d sk=%d", c.Uin, len(tok), len(d2), len(sk))
	}
	fmt.Fprintf(os.Stderr, "[credlogin] uin=%d a2=%dB d2=%dB sk=%dB uid=%s\n", c.Uin, len(tok), len(d2), len(sk), c.Uid)
	// Establish the connection and o3 state (session() runs the o3 delegation when enabled).
	if _, err := b.session(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	b.mu.Lock()
	b.account.Uin = c.Uin
	b.account.Token = tok
	b.account.D2 = d2
	b.account.SessionKey = sk
	b.account.UID = c.Uid
	connection := b.connection
	b.mu.Unlock()
	if setter, ok := connection.(interface{ SetSessionKey([]byte) }); ok {
		setter.SetSessionKey(sk)
	}
	return b.register(ctx)
}

// register announces the client, which is the step a login is not complete without: the
// credentials are the account's, and this is what makes the account reachable.
//
// It is an ordinary request and travels like one, which is why it can only be sent once the
// session carries the tickets the login produced — the same tickets that make the packet
// decodable at the far end at all.
func (b *Bot) register(ctx context.Context) error {
	exchange, err := b.service()
	if err != nil {
		return err
	}
	if status, ok := exchange.(interface{ ResetAuthenticationError() }); ok {
		status.ResetAuthenticationError()
	}
	b.reportLoginProgress("securing")
	if account, ok := b.options.Signer.(interface {
		Account(context.Context, uint32, bool) error
	}); ok {
		b.mu.Lock()
		uin := b.account.Uin
		b.mu.Unlock()
		if err := account.Account(ctx, uin, true); err != nil {
			return err
		}
		if _, token := signerSession(b.options.Signer); token != "" {
			b.o3Delegate(ctx, exchange, 30*time.Second, true)
		}
	}
	b.mu.Lock()
	input, device := b.options.Register, b.options.Device
	input.GUID = hex.EncodeToString(device.Guid)
	input.ServiceID = b.account.MsfID
	b.mu.Unlock()

	b.reportLoginProgress("registering")
	answer, err := exchange.Exchange(ctx, protocol.RegisterCommand, protocol.BuildRegister(input))
	if err != nil {
		return fmt.Errorf("announce the client: %w", err)
	}
	if err := protocol.ParseRegister(answer); err != nil {
		return err
	}

	b.mu.Lock()
	b.online = true
	b.terminalErr = nil
	b.recovery = bot.ReconnectStatus{}
	b.beat = startSSOHeartbeat(exchange, b.options.Heartbeat, b.heartbeatLogger())
	own := b.connection != nil
	selfUin, selfUid, selfNick := b.account.Uin, b.account.UID, b.nickname
	b.mu.Unlock()
	b.startO3Relay(exchange)
	// Teach the cache the bot's own uin↔uid, so a notice whose subject is the bot itself (it was
	// muted, say) resolves instead of reporting uin 0 — the bot never receives a message "from"
	// itself to learn this from.
	b.remember(selfUin, selfUid, selfNick)
	// Start consuming server pushes and broadcasting incoming messages to /ws listeners. Only
	// on a connection the bot owns (an injected exchange has no read loop to pull pushes from).
	if own {
		b.startConnectionLoop()
	}
	return nil
}

// SetLoginProgress reports stages without implying that registration is accepted.
func (b *Bot) SetLoginProgress(callback func(string)) {
	b.mu.Lock()
	b.loginProgress = callback
	b.mu.Unlock()
}
func (b *Bot) SetAccountGuard(guard func(uint32) error) {
	b.mu.Lock()
	b.accountGuard = guard
	b.mu.Unlock()
}
func (b *Bot) reportLoginProgress(phase string) {
	b.mu.Lock()
	callback := b.loginProgress
	b.mu.Unlock()
	if callback != nil {
		callback(phase)
	}
}

// Self reports the signed-in account.
//
// What it can report is what the login and the announcement yielded. A nickname is fetched
// from the service rather than carried by the login, and that call is not implemented, so it
// stays empty. Online is not "there are tickets": it is that the service accepted the
// announcement, which is the point at which it starts delivering anything.
func (b *Bot) Self(ctx context.Context) (bot.Self, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.credentials.SessionKey) == 0 {
		return bot.Self{}, bot.ErrNotConnected
	}
	if conn, ok := b.connection.(interface{ Closed() bool }); ok && conn.Closed() {
		b.online = false
	}
	if status, ok := b.exchange.(interface{ AuthenticationError() error }); ok && status.AuthenticationError() != nil {
		b.online = false
	}
	online := b.online && b.beat.HealthError() == nil
	return bot.Self{
		Uin:      b.account.Uin,
		UID:      b.account.UID,
		Nickname: b.nickname,
		Online:   online,
	}, nil
}

func (b *Bot) AuthenticationError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.terminalErr != nil {
		return b.terminalErr
	}
	if status, ok := b.exchange.(interface{ AuthenticationError() error }); ok {
		return status.AuthenticationError()
	}
	return nil
}

// Groups lists the account's groups.
//
// The list is asked for rather than waited on: it is an ordinary operation, and the account
// it belongs to travels in the service layer around it rather than in the request. An account
// with no groups answers with an empty list, which is a real answer and is reported as one —
// a refusal is what comes back as an error instead.
func (b *Bot) Groups(ctx context.Context) ([]bot.Group, error) {
	exchange, err := b.service()
	if err != nil {
		return nil, err
	}
	answer, err := exchange.Exchange(ctx, protocol.FetchGroupsCommand, protocol.BuildFetchGroups())
	if err != nil {
		return nil, err
	}
	groups, err := protocol.ParseGroups(answer)
	if err != nil {
		return nil, err
	}
	out := make([]bot.Group, 0, len(groups))
	for _, group := range groups {
		out = append(out, bot.Group{
			GroupUin:       group.Uin,
			GroupName:      group.Name,
			MemberCount:    int(group.MemberCount),
			MaxMemberCount: int(group.MemberMax),
			CreateTime:     group.CreateTime, Remark: group.Remark, Memo: group.Memo,
			Description: group.Description, ShutUpAllTimestamp: group.ShutUpAllTimestamp,
		})
	}
	return out, nil
}

// Call runs a named protocol operation.
//
// The operations are OIDB services, which are addressed by name and carry a protobuf that is
// the operation. This knows how to address one and read the answer; what it does not know is
// what any particular one says, so the caller supplies the body and reads the answer's. That
// is enough to drive an operation from outside while its own encoding is worked out, and it
// is what makes the envelope worth having on its own.
//
// The parameters are the body and the envelope's reserved field, both as they should go out:
//
//	{"body": "0a18...", "reserved": 1}
//
// The result is the answer's body, in the same form.
func (b *Bot) Call(ctx context.Context, function string, params json.RawMessage) (any, error) {
	command, err := protocol.ParseOIDBCommand(function)
	if err != nil {
		return nil, err
	}
	var call struct {
		Body     string `json:"body"`
		Reserved uint64 `json:"reserved"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &call); err != nil {
			return nil, fmt.Errorf("call %s: %w", function, err)
		}
	}
	body, err := hex.DecodeString(strings.TrimSpace(call.Body))
	if err != nil {
		return nil, fmt.Errorf("call %s: the body is not hex: %w", function, err)
	}

	exchange, err := b.service()
	if err != nil {
		return nil, err
	}
	// An OIDB request is not framed: its envelope travels in the service layer's body and
	// nothing else, which is why it needs neither a frame nor the agreed key.
	answer, err := exchange.Exchange(ctx, command.String(),
		protocol.BuildOIDB(protocol.OIDBRequest{Command: command, Body: body, Reserved: call.Reserved}))
	if err != nil {
		return nil, err
	}
	unwrapped, err := protocol.UnwrapOIDB(answer)
	if err != nil {
		return nil, err
	}
	if unwrapped.Command != command {
		return nil, fmt.Errorf("call %s: the answer is from %s",
			function, unwrapped.Command.String())
	}
	// An operation that failed says so inside the envelope, with a code and usually a reason.
	// Passing its body on would hand the caller an empty result and no way to tell it from an
	// operation that had nothing to say.
	if unwrapped.Failed() {
		return nil, fmt.Errorf("call %s: refused with code %d: %s",
			function, unwrapped.ErrorCode, unwrapped.ErrorMessage)
	}
	return map[string]any{"body": hex.EncodeToString(unwrapped.Body), "reserved": unwrapped.Reserved}, nil
}

// service returns the exchange to run an operation over, or says that nothing is signed in.
func (b *Bot) service() (Exchange, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.exchange == nil || b.recovery.Reconnecting || b.terminalErr != nil {
		return nil, fmt.Errorf("%w: nothing has been signed in", bot.ErrNotConnected)
	}
	return b.exchange, nil
}

// Credentials reports what the login yielded, for a caller that wants the tickets.
func (b *Bot) Credentials() protocol.Credentials {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.credentials
}

// session returns the flow, connecting and agreeing a key the first time it is asked.
func (b *Bot) session(ctx context.Context) (*QRLogin, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.recovery.Reconnecting || b.terminalErr != nil {
		return nil, fmt.Errorf("%w: disconnect this session before requesting a new QR code", bot.ErrNotConnected)
	}
	if b.login != nil {
		return b.login, nil
	}
	exchange := b.options.Exchange
	if exchange == nil {
		if b.options.Dial == nil || b.options.Signer == nil {
			return nil, fmt.Errorf("the bot is not wired to a connection and a signer")
		}
		if prepared, ok := b.options.Signer.(interface {
			Prepare(context.Context, []byte, uint32) error
		}); ok {
			if err := prepared.Prepare(ctx, b.options.Device.Guid, b.options.ExpectedUin); err != nil {
				return nil, err
			}
		}
		connection, err := b.options.Dial(ctx)
		if err != nil {
			return nil, fmt.Errorf("connect: %w", err)
		}
		exchange = NewServiceExchange(connection, b.options.Signer, &b.account)
		b.connection = connection
	}
	// Establish the login key agreement before anything else is asked for. The service expects
	// a client to perform this exchange on a fresh connection; until it has, a QR fetch is read
	// as coming from a session that was never set up and is dropped without an answer. The
	// agreement's own key is not kept here: pre-login frames stay under the empty key (the
	// service layer's "EmptyKey" form), and only a login installs a session key. Doing the
	// exchange is what the working references do at connect time, and it is what was missing.
	if b.connection != nil && os.Getenv("OPENOPQ_NO_KEYEXCHANGE") == "" {
		if err := b.keyExchange(ctx, exchange); err != nil {
			return nil, fmt.Errorf("establish the login key agreement: %w", err)
		}
	}
	// Diagnostic: send captured o3 handshake bodies on the greeted connection to learn whether
	// the service accepts a re-signed replay of them before a fetch. Gated by OPENOPQ_O3_PROBE.
	if manifest := strings.TrimSpace(os.Getenv("OPENOPQ_O3_PROBE")); manifest != "" {
		b.o3Probe(ctx, exchange, manifest)
	}
	// Relay this SDK process's own pre-login callbacks on the matching connection.
	// Device tokens and post-login callbacks may only become available after a scan.
	_, signerToken := signerSession(b.options.Signer)
	if os.Getenv("OPENOPQ_O3_DELEGATE") != "" || signerToken != "" {
		b.o3Delegate(ctx, exchange, 8*time.Second, false)
	}
	login, err := NewQRLogin(exchange, b.options.Device, QRLoginOptions{})
	if err != nil {
		return nil, err
	}
	// A keep-alive only makes sense on a connection the bot owns: on an injected exchange
	// there is nothing here that knows how to send one.
	if b.connection != nil {
		keep, err := startKeepAlive(b.connection, b.options.Token, b.options.KeepAlive)
		if err != nil {
			return nil, fmt.Errorf("start the keep-alive: %w", err)
		}
		b.keepAlive = keep
	}
	b.exchange = exchange
	b.login = login
	return login, nil
}

// o3Probe sends captured o3 handshake bodies as signed SSO commands on the greeted
// connection, to learn whether the service accepts a re-signed replay of them. It is a
// diagnostic gated by OPENOPQ_O3_PROBE, which names a manifest file. Each non-comment line is
// a command name and a path to a file holding that command's raw body, separated by
// whitespace; the body is signed afresh for this connection's sequence and sent, and the
// reply (or the refusal code and message) is logged to stderr. It never fails the session: a
// probe is run to observe, not to gate the flow.
func (b *Bot) o3Probe(ctx context.Context, exchange Exchange, manifest string) {
	data, err := os.ReadFile(manifest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[o3probe] cannot read manifest %s: %v\n", manifest, err)
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			fmt.Fprintf(os.Stderr, "[o3probe] skipping malformed line: %q\n", line)
			continue
		}
		command, bodyPath := fields[0], fields[1]
		body, err := os.ReadFile(bodyPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[o3probe] %s: cannot read body %s: %v\n", command, bodyPath, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "[o3probe] sending %s (%d bytes)\n", command, len(body))
		reply, err := exchange.Exchange(ctx, command, body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[o3probe] %s -> ERROR: %v\n", command, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "[o3probe] %s -> OK, reply %d bytes: %s\n",
			command, len(reply), hex.EncodeToString(reply))
	}
}

// o3Delegate relays SDK callbacks and replies over this login's connection.
// Observing a particular command is a queue milestone, not proof of login;
// online status still requires an accepted Register response.
//
// The signer base URL is OPENOPQ_O3_SIGNER (default http://127.0.0.1:18094, the tunnel to
// the in-process engine). It is a diagnostic/bring-up path and never fails the session.
func signerSession(signer Signer) (string, string) {
	if value, ok := signer.(interface{ O3Session() (string, string) }); ok {
		return value.O3Session()
	}
	return "", ""
}
func (b *Bot) startO3Relay(exchange Exchange) {
	if b.options.DisableO3Relay {
		return
	}
	_, token := signerSession(b.options.Signer)
	if token == "" {
		return
	}
	b.mu.Lock()
	if b.o3Cancel != nil || !b.online {
		b.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.o3Cancel = cancel
	done := make(chan struct{})
	b.o3Done = done
	b.mu.Unlock()

	go func() {
		defer close(done)
		defer func() {
			b.mu.Lock()
			if b.o3Done == done {
				b.o3Cancel = nil
				b.o3Done = nil
			}
			b.mu.Unlock()
		}()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			if b.AuthenticationError() != nil {
				return
			}
			b.o3DelegateWithTrigger(ctx, exchange, 6*time.Second, false, false)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (b *Bot) o3Delegate(ctx context.Context, exchange Exchange, budget time.Duration, authenticated bool) {
	b.o3DelegateWithTrigger(ctx, exchange, budget, authenticated, true)
}

func (b *Bot) o3DelegateWithTrigger(ctx context.Context, exchange Exchange, budget time.Duration, authenticated, trigger bool) {
	if b.options.DisableO3Relay {
		fmt.Fprintln(os.Stderr, "[o3deleg] relay disabled for isolated research")
		return
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("OPENOPQ_O3_SIGNER")), "/")
	managedBase, sessionToken := signerSession(b.options.Signer)
	if sessionToken != "" {
		base = managedBase
	}
	if base == "" {
		base = "http://127.0.0.1:18094"
	}
	client := &http.Client{Timeout: 12 * time.Second}
	doRequest := func(req *http.Request) (*http.Response, error) {
		if managed, ok := b.options.Signer.(interface {
			DoO3(context.Context, string, string, []byte) (*http.Response, error)
		}); ok && sessionToken != "" {
			var payload []byte
			if req.Body != nil {
				var err error
				payload, err = io.ReadAll(io.LimitReader(req.Body, 8*1024*1024))
				req.Body.Close()
				if err != nil {
					return nil, err
				}
			}
			return managed.DoO3(req.Context(), req.Method, strings.TrimPrefix(req.URL.String(), base), payload)
		}
		return client.Do(req)
	}
	if trigger {
		// Notify the SDK that its host is ready; it may schedule additional main-queue work.
		if req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/o3/trigger", nil); err == nil {
			if sessionToken != "" {
				req.Header.Set("X-OPQ-Session", sessionToken)
			}
			if resp, err := doRequest(req); err == nil {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				fmt.Fprintf(os.Stderr, "[o3deleg] trigger -> %s\n", strings.TrimSpace(string(body)))
			} else {
				fmt.Fprintf(os.Stderr, "[o3deleg] trigger error: %v\n", err)
			}
		}
		if !waitRecovery(ctx, 500*time.Millisecond) {
			return
		}
	}
	// Drain pre-login work briefly; after authentication allow time for access callbacks.
	// QQ 7.0.2 can use SsoSecureA2Establish rather than the older SsoSecureAccess name.
	empties := 0
	secureDone := false
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/o3/pull", nil)
		if sessionToken != "" {
			req.Header.Set("X-OPQ-Session", sessionToken)
		}
		resp, err := doRequest(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[o3deleg] pull error: %v\n", err)
			return
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			fmt.Fprintf(os.Stderr, "[o3deleg] pull returned HTTP %d\n", resp.StatusCode)
			return
		}
		pull, body, decodeErr := readO3Pull(resp.Body)
		resp.Body.Close()
		if decodeErr != nil {
			fmt.Fprintln(os.Stderr, "[o3deleg] invalid pull response")
			return
		}
		if pull.Empty {
			empties++
			// Only stop on quiet once SsoSecureAccess has completed. Before that, a pause is
			// just the engine between phases, not the end of the handshake.
			if (secureDone || !authenticated) && empties >= 4 {
				fmt.Fprintf(os.Stderr, "[o3deleg] queue drained (secureDone=%v, authenticated=%v)\n", secureDone, authenticated)
				return
			}
			if !waitRecovery(ctx, 500*time.Millisecond) {
				return
			}
			continue
		}
		empties = 0
		fmt.Fprintf(os.Stderr, "[o3deleg] -> send subseq=%s cmd=%s len=%d\n", pull.Subseq, pull.Cmd, len(body))
		reply, err := exchange.Exchange(ctx, pull.Cmd, body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[o3deleg]    exchange error: %v\n", err)
			continue
		}
		replyPayload, err := o3ReplyPayload(reply)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[o3deleg]    invalid SSO reply framing: %v\n", err)
			continue
		}
		// The server accepted the command (Exchange only returns a body on a zero code), so the
		// return code fed back to the engine is success. The old code fed 1 here, a non-zero
		// code the engine reads as a failed step — which is why it kept retrying the share-key
		// establishment and never treated the handshake as done.
		fmt.Fprintf(os.Stderr, "[o3deleg]    reply payload %d bytes (rc=0), feeding to libsecO3OnReceive\n", len(replyPayload))
		feed, _ := json.Marshal(map[string]any{
			"subseq": pull.Subseq, "cmd": pull.Cmd, "rc": 0,
			"data": base64.StdEncoding.EncodeToString(replyPayload),
		})
		fr, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/o3/recv", bytes.NewReader(feed))
		fed := false
		if err == nil {
			fr.Header.Set("Content-Type", "application/json")
			if sessionToken != "" {
				fr.Header.Set("X-OPQ-Session", sessionToken)
			}
			if rr, err := doRequest(fr); err == nil {
				fed = rr.StatusCode == http.StatusOK
				rr.Body.Close()
			}
		}
		if !fed {
			fmt.Fprintln(os.Stderr, "[o3deleg] SDK did not acknowledge the response")
			continue
		}
		if o3SecureAccessCommand(pull.Cmd) {
			secureDone = true
			fmt.Fprintf(os.Stderr, "[o3deleg] secure access response relayed to SDK\n")
		}
	}
	fmt.Fprintf(os.Stderr, "[o3deleg] deadline reached (secureDone=%v)\n", secureDone)
}

// keyExchange performs the login ECDH agreement (trpc.login.ecdh.EcdhService.SsoKeyExchange)
// on a fresh connection. The exchange's own key and token are not retained: pre-login frames
// stay under the empty key, so what this buys is the server-side state the service wants to
// see before it will answer a request — without it a QR fetch is dropped unanswered.
//
// It is sent like any other request, under the empty key, because at this point there is no
// session key yet; the response is unpacked only to confirm the agreement completed.
func (b *Bot) keyExchange(ctx context.Context, exchange Exchange) error {
	request, err := protocol.BuildKeyExchange(protocol.KeyExchange{
		Uin:       b.account.Uin,
		Guid:      b.options.Device.Guid,
		Timestamp: uint32(b.account.now().Unix()),
	})
	if err != nil {
		return err
	}
	response, err := exchange.Exchange(ctx, protocol.KeyExchangeCommand, request.Body)
	if err != nil {
		return err
	}
	if _, err := protocol.UnpackKeyExchange(response, request.PrivateKey); err != nil {
		return fmt.Errorf("the agreement did not complete: %w", err)
	}
	return nil
}

// Close drops the connection. A bot that has been closed may be used again, and will make a
// fresh connection and a fresh agreement when it is.
func (b *Bot) Close() error {
	return b.closeConnection(true)
}

// Suspend closes the QQ connection while retaining the SDK resume checkpoint.
// Only process shutdown uses this; explicit disconnect still releases the SDK.
func (b *Bot) Suspend() error {
	saved := b.SavedLoginStatus()
	return b.closeConnection(!saved.Restorable || !saved.AutoResume)
}

func (b *Bot) closeConnection(release bool) error {
	b.closeMu.Lock()
	defer b.closeMu.Unlock()
	defer b.cleanMedia()
	b.mu.Lock()
	cancel, done := b.recoveryCancel, b.recoveryDone
	b.recoveryCancel = nil
	b.recovery = bot.ReconnectStatus{}
	b.terminalErr = nil
	connection, keep, beat, stopO3, o3Done := b.connection, b.keepAlive, b.beat, b.o3Cancel, b.o3Done
	b.connection, b.exchange, b.login, b.keepAlive, b.beat, b.o3Cancel, b.o3Done = nil, nil, nil, nil, nil, nil, nil
	b.online = false
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if stopO3 != nil {
		stopO3()
	}
	var err error
	if closer, ok := connection.(interface{ Close() error }); ok {
		err = closer.Close()
	}
	beat.Close()
	keep.Close()
	if o3Done != nil {
		<-o3Done
	}
	if done != nil {
		<-done
	}
	b.subs.closeAll()
	if signer, ok := b.options.Signer.(interface{ Release() error }); ok && release {
		if releaseErr := signer.Release(); err == nil {
			err = releaseErr
		}
	}
	return err
}

// Await connects and runs a login to completion, which is what a caller with a code to show
// wants.
func (b *Bot) Await(ctx context.Context, show func(image []byte) error) (protocol.Credentials, error) {
	image, err := b.QRCode(ctx)
	if err != nil {
		return protocol.Credentials{}, err
	}
	if show != nil {
		if err := show(image); err != nil {
			return protocol.Credentials{}, err
		}
	}
	if err := b.LoadSession(ctx); err != nil {
		return protocol.Credentials{}, err
	}
	return b.Credentials(), nil
}

// Session returns the account details the bot has settled on, so a caller can persist them.
func (b *Bot) Session() Session {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.account
}

// Device is the QR device block this bot reports.
func (b *Bot) Device() protocol.QRDevice {
	return b.options.Device
}

// RegisterInput is the announcement this bot will send at the end of a login.
func (b *Bot) RegisterInput() protocol.RegisterInput {
	return b.options.Register
}
