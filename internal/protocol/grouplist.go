package protocol

import "fmt"

// The group list.
//
// The list does not arrive on its own. An earlier reading of the flow had it pushed by a sync
// the service decides to send, and that was wrong: a client asks for it, with an ordinary
// OIDB operation, and reads the answer like any other. Nothing about it is a special case of
// the connection, which is why it can be built out of the envelope that is already here.
//
// The operation is OidbSvcTrpcTcp.0xfe5_2, and what it is asked for is mostly a list of flags
// — one per field the service knows how to report — in three blocks. The request carries no
// account: that travels in the service layer around it.
//
// The request below is the one a client was captured sending, field for field and byte for
// byte. It is worth saying why that matters, because a published implementation states this
// operation too and the two disagree in three places: it asks for thirty fields where the
// client asks for thirty-five, it asks its third block for two fields where the client asks
// for six, and it sets the envelope's reserved field where the client leaves it clear. The
// capture is what settled all three, and the reference's version is not what is built here.
//
// The answer is the same envelope around a repeated field of groups, so reading it needs a
// reader that keeps repeats rather than the last value. That reader is EveryField, and it is
// the reason this file did not exist earlier.
const (
	// FetchGroupsCommand is the operation a client asks for its group list with.
	FetchGroupsCommand = OIDBNamePrefix + "0xfe5_2"

	// The blocks a fetch is made of.
	fetchGroupsConfigField = 1 // the request: the config, and nothing else
	fetchGroupsBlock1Field = 1
	fetchGroupsBlock2Field = 2
	fetchGroupsBlock3Field = 3

	// The answer: a repeated list of groups, each of them a number and two blocks.
	groupListField        = 2
	groupUinField         = 3
	groupInfoField        = 4
	groupExtInfoField     = 5
	groupCreateTimeField  = 2
	groupMemberMaxField   = 3
	groupMemberCountField = 4
	groupNameField        = 5
	groupRemarkField      = 3
)

// fetchGroupsBlock1Flags is the first block up to the one field in it that is not a flag.
//
// The gaps are the client's: it leaves out six, seven and twenty-one of the low fields, and
// then asks for thirty-nine and forty-one — numbers the published definition does not have at
// all. Nothing here says what any of them selects, so they are carried as they were sent
// rather than tidied into the shape a definition implies.
var fetchGroupsBlock1Flags = []int{
	1, 2, 3, 4, 5, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 22, 23, 24, 25, 26, 27,
	28, 29, 30, 31, 32, 39, 41,
}

// fetchGroupsBlock1Message is the one field in the first block that is a message rather than a
// flag: a block of its own holding a single field, set. It is written between the two runs of
// flags because the capture puts it there.
const fetchGroupsBlock1Message = 4000

// fetchGroupsBlock1Tail is the rest of the block, asked for as flags like the first run.
var fetchGroupsBlock1Tail = []int{5001, 5002, 5003}

// fetchGroupsBlock2 and fetchGroupsBlock3 complete the request. The third asks for six fields,
// which is four more than the published definition of this operation gives it.
var fetchGroupsBlock2 = []int{1, 2, 3, 4, 5, 6, 7, 8}
var fetchGroupsBlock3 = []int{2, 3, 5, 6, 7, 8}

// Group is one group as the service reports it. What a group is called and how big it is are
// the two things every caller wants, so they are lifted out of the blocks they travel in; the
// rest of the report is left where it is rather than guessed at.
type Group struct {
	Uin                uint32
	Name               string
	MemberCount        uint32
	MemberMax          uint32
	CreateTime         uint32
	Memo               string
	Remark             string
	Description        string
	ShutUpAllTimestamp uint32
}

// BuildFetchGroups builds the body of a fetch: the envelope, around a request that is a config
// and one field beside it.
//
// The reserved field is left clear, which is what the captured request does. A published
// implementation sets it here, and whether that is a different build of the client or simply
// a mistake in that implementation is not something this can tell.
func BuildFetchGroups() []byte {
	config := Marshal(
		BytesField(fetchGroupsBlock1Field, fetchGroupsBlock1()),
		BytesField(fetchGroupsBlock2Field, flags(fetchGroupsBlock2)),
		BytesField(fetchGroupsBlock3Field, flags(fetchGroupsBlock3)),
	)
	return BuildOIDB(OIDBRequest{
		Command: OIDBCommand{Service: 0xFE5, SubCommand: 2},
		Body: Marshal(
			BytesField(fetchGroupsConfigField, config),
			BytesField(fetchGroupsBodyExtraField, fetchGroupsBodyExtra()),
		),
	})
}

// fetchGroupsBodyExtraField is a second field in the request, after the config. The published
// definition of this operation does not have it at all, which is the third thing the capture
// settled that no definition could.
const fetchGroupsBodyExtraField = 3

// fetchGroupsBodyExtra is that field: one field set and one left empty, neither of which
// anything here says the meaning of. It is carried as it was sent.
func fetchGroupsBodyExtra() []byte {
	return Marshal(VarintField(1, 1), BytesField(15, nil))
}

// fetchGroupsBlock1 is the first block: flags, the one message field, and flags again, in the
// order the capture has them.
func fetchGroupsBlock1() []byte {
	out := flags(fetchGroupsBlock1Flags)
	out = append(out, Marshal(BytesField(fetchGroupsBlock1Message,
		Marshal(VarintField(1, 1))))...)
	return append(out, flags(fetchGroupsBlock1Tail)...)
}

// flags renders a list of field numbers as every one of them being asked for.
func flags(tags []int) []byte {
	fields := make([]Field, 0, len(tags))
	for _, tag := range tags {
		fields = append(fields, VarintField(tag, 1))
	}
	return Marshal(fields...)
}

// ParseGroups reads the group list out of a fetch's answer.
//
// An answer that reports an error is reported as an error here rather than as a list with
// nothing in it, because an empty list is a real answer — an account with no groups — and a
// caller that could not tell the two apart would take a failure for a fact.
func ParseGroups(answer []byte) ([]Group, error) {
	envelope, err := UnwrapOIDB(answer)
	if err != nil {
		return nil, err
	}
	if envelope.Command != (OIDBCommand{Service: 0xFE5, SubCommand: 2}) {
		return nil, fmt.Errorf("protocol: the group list came back from %s", envelope.Command)
	}
	if envelope.Failed() {
		return nil, fmt.Errorf("protocol: the group list was refused with code %d: %s",
			envelope.ErrorCode, envelope.ErrorMessage)
	}
	fields, err := EveryField(envelope.Body)
	if err != nil {
		return nil, fmt.Errorf("protocol: group list: %w", err)
	}
	entries := FieldsWith(fields, groupListField)
	groups := make([]Group, 0, len(entries))
	for _, entry := range entries {
		group, err := parseGroup(entry.Data)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, nil
}

// parseGroup reads one entry. A block that is missing is left at its zero value rather than
// refused: the request asks for every field, but a service that reports less is still
// reporting something worth having.
func parseGroup(entry []byte) (Group, error) {
	fields, err := EveryField(entry)
	if err != nil {
		return Group{}, fmt.Errorf("protocol: group: %w", err)
	}
	group := Group{}
	if uin, ok := FirstField(fields, groupUinField); ok {
		group.Uin = uint32(uin.Number)
	}
	if info, ok := FirstField(fields, groupInfoField); ok {
		group.Name, group.MemberCount, group.MemberMax, group.CreateTime, err = parseGroupInfo(info.Data)
		if err != nil {
			return Group{}, err
		}
		detail, _ := EveryField(info.Data)
		group.Description = pbStr(detail, 18)
		group.Memo = pbStr(detail, 30)
		group.ShutUpAllTimestamp = pbU32(detail, 10)
	}
	if ext, ok := FirstField(fields, groupExtInfoField); ok {
		group.Remark, err = parseGroupExtInfo(ext.Data)
		if err != nil {
			return Group{}, err
		}
	}
	return group, nil
}

// parseGroupInfo reads the block a group's name and size travel in.
func parseGroupInfo(block []byte) (name string, memberCount, memberMax, createTime uint32, err error) {
	fields, err := EveryField(block)
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("protocol: group info: %w", err)
	}
	if field, ok := FirstField(fields, groupNameField); ok {
		name = string(field.Data)
	}
	if field, ok := FirstField(fields, groupMemberCountField); ok {
		memberCount = uint32(field.Number)
	}
	if field, ok := FirstField(fields, groupMemberMaxField); ok {
		memberMax = uint32(field.Number)
	}
	if field, ok := FirstField(fields, groupCreateTimeField); ok {
		createTime = uint32(field.Number)
	}
	return name, memberCount, memberMax, createTime, nil
}

// parseGroupExtInfo reads the account-local group remark, not its description.
func parseGroupExtInfo(block []byte) (string, error) {
	fields, err := EveryField(block)
	if err != nil {
		return "", fmt.Errorf("protocol: group ext info: %w", err)
	}
	if field, ok := FirstField(fields, groupRemarkField); ok {
		return string(field.Data), nil
	}
	return "", nil
}
