package protocol

import "fmt"

// The credentials the web services need.
//
// Two operations hand out what the HTTP APIs are called with rather than what the connection
// is: a cookie for a domain, and the client key a web endpoint is called with. Neither is on
// the messaging path, and both are what makes the web half of the framework reachable at all.
//
// Their requests are the whole of them: one is a domain and the other is nothing. Both were
// captured as they were signed, and both are rebuilt byte for byte from those captures — which
// is worth saying because the cookie fetch is the second operation here where a published
// implementation and the client disagree, in this case over the envelope's reserved field.
const (
	// FetchCookieCommand asks for the cookies of one or more domains.
	FetchCookieCommand = OIDBNamePrefix + "0x102a_0"
	// FetchClientKeyCommand asks for the key a web endpoint is called with.
	FetchClientKeyCommand = OIDBNamePrefix + "0x102a_1"

	fetchCookieDomainField = 1
	fetchCookieURLsField   = 1
	cookiePropertyName     = 1
	cookiePropertyValue    = 2

	clientKeyField       = 3
	clientKeyExpiryField = 4
)

// fetchCookieReserved is the envelope's reserved field for a cookie fetch. The captured
// request sets it; a published implementation leaves it clear for the same operation.
const fetchCookieReserved uint64 = 1

// BuildFetchCookie asks for the cookies of one domain, for example pd.qq.com.
func BuildFetchCookie(domain string) []byte {
	return BuildOIDB(OIDBRequest{
		Command:  OIDBCommand{Service: 0x102A, SubCommand: 0},
		Body:     Marshal(StringField(fetchCookieDomainField, domain)),
		Reserved: fetchCookieReserved,
	})
}

// Cookie is one cookie as the service hands it over. The name is the domain that was asked
// for, which is what makes the value usable.
type Cookie struct {
	Name  string
	Value string
}

// ParseCookies reads the cookies out of an answer.
//
// An answer that has none is an empty list rather than a failure: a domain the account has no
// cookie for is a real answer, and a caller that could not tell it from a refusal would treat
// a missing cookie as a broken session.
func ParseCookies(answer []byte) ([]Cookie, error) {
	envelope, err := UnwrapOIDB(answer)
	if err != nil {
		return nil, err
	}
	if err := checkOperation(envelope, OIDBCommand{Service: 0x102A, SubCommand: 0}); err != nil {
		return nil, err
	}
	fields, err := EveryField(envelope.Body)
	if err != nil {
		return nil, fmt.Errorf("protocol: cookies: %w", err)
	}
	entries := FieldsWith(fields, fetchCookieURLsField)
	cookies := make([]Cookie, 0, len(entries))
	for _, entry := range entries {
		property, err := EveryField(entry.Data)
		if err != nil {
			return nil, fmt.Errorf("protocol: cookie: %w", err)
		}
		cookie := Cookie{}
		if field, ok := FirstField(property, cookiePropertyName); ok {
			cookie.Name = string(field.Data)
		}
		if field, ok := FirstField(property, cookiePropertyValue); ok {
			cookie.Value = string(field.Data)
		}
		cookies = append(cookies, cookie)
	}
	return cookies, nil
}

// BuildFetchClientKey asks for the key a web endpoint is called with. The request carries
// nothing: everything it needs is the account, which travels in the service layer around it.
func BuildFetchClientKey() []byte {
	return BuildOIDB(OIDBRequest{Command: OIDBCommand{Service: 0x102A, SubCommand: 1}})
}

// ClientKey is what a web endpoint is called with, and when it stops being usable.
type ClientKey struct {
	Key      string
	ExpireAt uint32
}

// ParseClientKey reads the key out of an answer. A key of nothing is not a key, so it is a
// failure rather than an empty result.
func ParseClientKey(answer []byte) (ClientKey, error) {
	envelope, err := UnwrapOIDB(answer)
	if err != nil {
		return ClientKey{}, err
	}
	if err := checkOperation(envelope, OIDBCommand{Service: 0x102A, SubCommand: 1}); err != nil {
		return ClientKey{}, err
	}
	fields, err := EveryField(envelope.Body)
	if err != nil {
		return ClientKey{}, fmt.Errorf("protocol: client key: %w", err)
	}
	key := ClientKey{}
	if field, ok := FirstField(fields, clientKeyField); ok {
		key.Key = string(field.Data)
	}
	if field, ok := FirstField(fields, clientKeyExpiryField); ok {
		key.ExpireAt = uint32(field.Number)
	}
	if key.Key == "" {
		return ClientKey{}, fmt.Errorf("protocol: the answer carries no client key")
	}
	return key, nil
}

// checkOperation confirms an answer came from the operation that was asked, because a reply
// from another service decoded as this one's would be read as this one's result.
func checkOperation(envelope OIDBRequest, want OIDBCommand) error {
	if envelope.Command != want {
		return fmt.Errorf("protocol: the answer is from %s, want %s", envelope.Command, want)
	}
	if envelope.Failed() {
		return fmt.Errorf("protocol: the service refused with code %d: %s",
			envelope.ErrorCode, envelope.ErrorMessage)
	}
	return nil
}
