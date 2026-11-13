package protocol

import "fmt"

// History and read-report packets. Shapes follow LagrangeGo's message.proto /
// action.proto (SsoReadedReport, SsoGetGroupMsg, SsoGetRoamMsg).

const (
	ReadedReportCommand = "trpc.msg.msg_svc.MsgService.SsoReadedReport"
	GetGroupMsgCommand  = "trpc.msg.register_proxy.RegisterProxy.SsoGetGroupMsg"
	GetRoamMsgCommand   = "trpc.msg.register_proxy.RegisterProxy.SsoGetRoamMsg"
)

// BuildReadedReportGroup marks a group conversation read from startSeq:
// SsoReadedReport{ Group(1){ GroupUin(1), StartSequence(2) } }.
func BuildReadedReportGroup(groupUin, startSeq uint32) []byte {
	return Marshal(BytesField(1, Marshal(
		VarintField(1, uint64(groupUin)),
		VarintField(2, uint64(startSeq)),
	)))
}

// BuildReadedReportC2C marks a private conversation read:
// SsoReadedReport{ C2C(2){ TargetUid(2), Time(3), StartSequence(4) } }.
func BuildReadedReportC2C(targetUID string, timestamp, startSeq uint32) []byte {
	return Marshal(BytesField(2, Marshal(
		StringField(2, targetUID),
		VarintField(3, uint64(timestamp)),
		VarintField(4, uint64(startSeq)),
	)))
}

// BuildGetGroupMsg asks for group messages in [startSeq, endSeq]:
// SsoGetGroupMsg{ Info(1){ GroupUin(1), StartSequence(2), EndSequence(3) }, Direction(2)=true }.
func BuildGetGroupMsg(groupUin, startSeq, endSeq uint32) []byte {
	return Marshal(
		BytesField(1, Marshal(
			VarintField(1, uint64(groupUin)),
			VarintField(2, uint64(startSeq)),
			VarintField(3, uint64(endSeq)),
		)),
		VarintField(2, 1),
	)
}

// BuildGetRoamMsg asks for recent private messages ending at timestamp:
// SsoGetRoamMsg{ FriendUid(1), Time(2), Random(3)=0, Count(4), Direction(5)=true }.
func BuildGetRoamMsg(friendUID string, timestamp, count uint32) []byte {
	return Marshal(
		StringField(1, friendUID),
		VarintField(2, uint64(timestamp)),
		VarintField(3, 0),
		VarintField(4, uint64(count)),
		VarintField(5, 1),
	)
}

// ParseGetGroupMsgResponse reads SsoGetGroupMsgResponse{ Body(3){ Messages(6)[] } }.
func ParseGetGroupMsgResponse(answer []byte) ([]*IncomingMessage, error) {
	fields, err := EveryField(stripSSOBodyLength(answer))
	if err != nil {
		return nil, fmt.Errorf("protocol: group history: %w", err)
	}
	body, err := EveryField(pbBytes(fields, 3))
	if err != nil {
		return nil, fmt.Errorf("protocol: group history body: %w", err)
	}
	return decodeHistoryMessages(FieldsWith(body, 6))
}

// ParseGetRoamMsgResponse reads SsoGetRoamMsgResponse{ Messages(7)[] }.
func ParseGetRoamMsgResponse(answer []byte) ([]*IncomingMessage, error) {
	fields, err := EveryField(stripSSOBodyLength(answer))
	if err != nil {
		return nil, fmt.Errorf("protocol: friend history: %w", err)
	}
	return decodeHistoryMessages(FieldsWith(fields, 7))
}

func decodeHistoryMessages(elems []Field) ([]*IncomingMessage, error) {
	out := []*IncomingMessage{}
	for _, elem := range elems {
		if !elem.IsData {
			continue
		}
		fields, err := EveryField(elem.Data)
		if err != nil {
			return nil, fmt.Errorf("protocol: history message: %w", err)
		}
		m := decodeMessageBody(fields)
		if m == nil || m.MsgSeq == 0 {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}
