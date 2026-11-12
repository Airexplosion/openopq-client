package protocol

import "fmt"

// The notifications the service holds for an account: friend requests and group invitations,
// as a list the client asks for rather than one it is pushed.
//
// The request is a count and one more number, both of which the captured request carries. What
// the second one is for is not named by anything here, so it is written as it was sent rather
// than left out — an absent field and a zero are different packets.
const (
	// FetchNotificationsCommand asks for the account's pending requests.
	FetchNotificationsCommand = OIDBNamePrefix + "0x10c0_1"

	notificationsCountField = 1
	// notificationsFromField is the second half of the request, which the capture leaves at
	// zero.
	notificationsFromField = 2

	notificationsListField      = 1
	notificationsLatestSeqField = 3
	notificationSequenceField   = 1
	notificationEventTypeField  = 2
	notificationStateField      = 3
	notificationGroupField      = 4
	notificationTargetField     = 5
)

// BuildFetchNotifications asks for up to count of the account's pending requests.
func BuildFetchNotifications(count uint32) []byte {
	return BuildOIDB(OIDBRequest{
		Command: OIDBCommand{Service: 0x10C0, SubCommand: 1},
		Body: Marshal(
			VarintField(notificationsCountField, uint64(count)),
			VarintField(notificationsFromField, 0),
		),
	})
}

// Notification is one pending request as the service reports it. The sequence is what a caller
// quotes back when it acts on one, and the state is what the service thinks of it so far.
//
// What the group and target blocks carry is left where it is rather than read: naming their
// fields would be guessing at them, and the three that are named here are the ones every
// caller needs.
type Notification struct {
	Sequence  uint64
	EventType uint32
	State     uint32
}

// Notifications is an answer to a fetch.
type Notifications struct {
	// LatestSequence is where the list had got to, which a caller that wants only what has
	// happened since can ask from.
	LatestSequence uint64
	Requests       []Notification
}

// ParseNotifications reads the list out of an answer.
func ParseNotifications(answer []byte) (Notifications, error) {
	envelope, err := UnwrapOIDB(answer)
	if err != nil {
		return Notifications{}, err
	}
	if err := checkOperation(envelope, OIDBCommand{Service: 0x10C0, SubCommand: 1}); err != nil {
		return Notifications{}, err
	}
	fields, err := EveryField(envelope.Body)
	if err != nil {
		return Notifications{}, fmt.Errorf("protocol: notifications: %w", err)
	}
	out := Notifications{}
	if field, ok := FirstField(fields, notificationsLatestSeqField); ok {
		out.LatestSequence = field.Number
	}
	for _, entry := range FieldsWith(fields, notificationsListField) {
		request, err := parseNotification(entry.Data)
		if err != nil {
			return Notifications{}, err
		}
		out.Requests = append(out.Requests, request)
	}
	return out, nil
}

func parseNotification(block []byte) (Notification, error) {
	fields, err := EveryField(block)
	if err != nil {
		return Notification{}, fmt.Errorf("protocol: notification: %w", err)
	}
	request := Notification{}
	if field, ok := FirstField(fields, notificationSequenceField); ok {
		request.Sequence = field.Number
	}
	if field, ok := FirstField(fields, notificationEventTypeField); ok {
		request.EventType = uint32(field.Number)
	}
	if field, ok := FirstField(fields, notificationStateField); ok {
		request.State = uint32(field.Number)
	}
	return request, nil
}
