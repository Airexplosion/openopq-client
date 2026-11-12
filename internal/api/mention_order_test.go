package api

import (
	"encoding/json"
	"strings"
	"testing"

	"openopq/internal/protocol"
)

func TestNativeMentionPositionsAndSingleCQRepresentation(t *testing.T) {
	at := protocol.AtElem(protocol.SendAt{Uin: 123, Uid: "u_bot", Nick: "机器人"})
	for _, tc := range []struct {
		name     string
		elems    [][]byte
		want     string
		mentions int
	}{
		{"command mention", [][]byte{protocol.TextElem(".r "), at, protocol.TextElem(" DN 伤害 + 1d6")}, ".r [CQ:at,qq=123] DN 伤害 + 1d6", 1},
		{"middle mention", [][]byte{protocol.TextElem(".r 1d6 + "), at, protocol.TextElem(" 的伤害")}, ".r 1d6 + [CQ:at,qq=123] 的伤害", 1},
		{"intentional repetition", [][]byte{at, protocol.TextElem(" 和 "), at}, "[CQ:at,qq=123] 和 [CQ:at,qq=123]", 2},
		{"literal at text", [][]byte{protocol.TextElem(".r @普通文字 + 1d6")}, ".r @普通文字 + 1d6", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fields []protocol.Field
			for _, elem := range tc.elems {
				fields = append(fields, protocol.BytesField(2, elem))
			}
			body := protocol.Marshal(protocol.BytesField(1, protocol.Marshal(protocol.BytesField(1, protocol.Marshal(protocol.VarintField(1, 456))), protocol.BytesField(3, protocol.Marshal(protocol.BytesField(1, protocol.Marshal(fields...)))))))
			m, err := protocol.DecodePushMessage(body)
			if err != nil {
				t.Fatal(err)
			}
			if got := cqMessage(m); got != tc.want {
				t.Fatalf("message reordered/duplicated: %q, want %q", got, tc.want)
			}
			var event map[string]any
			if err = json.Unmarshal(onebotEventJSON(protocol.MessageEvent{SelfUin: 123, Msg: m}), &event); err != nil {
				t.Fatal(err)
			}
			if event["raw_message"] != tc.want {
				t.Fatal("raw_message differed from message")
			}
			segments := cqSegments(tc.want)
			count := 0
			for _, seg := range segments {
				if seg["type"] == "at" {
					count++
				}
			}
			if count != tc.mentions || strings.Contains(tc.want, "@机器人") {
				t.Fatal("native mention display text repeated")
			}
		})
	}
}
