package protocol

// The account's friend requests.
//
// One of the two operations here is implemented and the other is not, and the difference is
// worth stating rather than hiding. A friend *request* fetch is a short request: the account
// and eight numbers, all of which the captured request and the published definition agree on.
// A friend *list* fetch is not: its request carries six blocks whose payloads are themselves
// encoded lists of codes, and reproducing it means decoding and re-encoding all of them for
// the client build that sent it. The capture of it is in `testdata/` and the definition is in
// the published protos, so it is work rather than a mystery — but it is not this.
//
// The account is named by a string rather than by its number: the client sends something like
// "u_wNGrZEQGXcmrlR4EVuuOOg", which is the form several services match on. Where that string
// comes from is a separate question — the login yields one, and whether it is this one is not
// established here.
const (
	// FetchFriendRequestsCommand asks for the account's pending friend requests.
	FetchFriendRequestsCommand = OIDBNamePrefix + "0x5cf_11"
	// FetchFriendsCommand asks for the account's friend list. Its request is not built here.
	FetchFriendsCommand = OIDBNamePrefix + "0xfd4_1"

	friendRequestsSelfUidField = 4

	friendRequestField1  = 1
	friendRequestField3  = 3
	friendRequestField5  = 5
	friendRequestField6  = 6
	friendRequestField8  = 8
	friendRequestField9  = 9
	friendRequestField12 = 12
	friendRequestField22 = 22
)

// BuildFetchFriendRequests asks for the account's pending friend requests.
//
// The eight numbers are carried as constants rather than as inputs because nothing observed
// varies them: the capture and the published definition give the same values, which is the one
// place in this package where the two agree without the capture having to correct it.
func BuildFetchFriendRequests(selfUid string) []byte {
	return BuildOIDB(OIDBRequest{
		Command: OIDBCommand{Service: 0x5CF, SubCommand: 11},
		Body: Marshal(
			VarintField(friendRequestField1, 1),
			VarintField(friendRequestField3, 6),
			StringField(friendRequestsSelfUidField, selfUid),
			VarintField(friendRequestField5, 0),
			VarintField(friendRequestField6, 80),
			VarintField(friendRequestField8, 2),
			VarintField(friendRequestField9, 0),
			VarintField(friendRequestField12, 1),
			VarintField(friendRequestField22, 1),
		),
	})
}
