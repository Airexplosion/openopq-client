package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"openopq/internal/protocol"
)

func ssoWrap(body []byte) []byte {
	out := make([]byte, 4+len(body))
	n := uint32(len(body) + 4)
	out[0], out[1], out[2], out[3] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
	copy(out[4:], body)
	return out
}

func TestSetFriendRemarkAndDeleteFriendPackets(t *testing.T) {
	var cmds []string
	var bodies [][]byte
	backend := NewOneBotQueryBackend(obQueryExchange(func(_ context.Context, cmd string, body []byte) ([]byte, error) {
		cmds = append(cmds, cmd)
		bodies = append(bodies, append([]byte(nil), body...))
		if cmd == protocol.OidbCommand(0x126b, 0) {
			return ssoWrap(protocol.Marshal(protocol.VarintField(1, 0x126b), protocol.VarintField(3, 0))), nil
		}
		return []byte{}, nil
	}))
	backend.remember(123, "u_del", "n")
	if _, err := backend.OneBotAction(context.Background(), "set_friend_remark", json.RawMessage(`{"user_id":123,"remark":"lab"}`)); err != nil {
		t.Fatal(err)
	}
	if cmds[0] != "ProfileService.ChangeFriendName" || !bytes.Contains(bodies[0], []byte("ChangeFriendName")) {
		t.Fatalf("remark packet %q %q", cmds[0], bodies[0])
	}
	if _, err := backend.OneBotAction(context.Background(), "delete_friend", json.RawMessage(`{"user_id":123,"block":true}`)); err != nil {
		t.Fatal(err)
	}
	if cmds[1] != "OidbSvcTrpcTcp.0x126b_0" {
		t.Fatal(cmds[1])
	}
	req, err := protocol.UnwrapOIDB(bodies[1])
	if err != nil {
		t.Fatal(err)
	}
	inner, _ := protocol.EveryField(obBytes(obFields(req.Body), 1))
	if obString(inner, 1) != "u_del" || obNumber(inner, 3) != 1 {
		t.Fatalf("delete body %+v", inner)
	}
}

func TestMarkReadAndHistoryPackets(t *testing.T) {
	backend := NewOneBotQueryBackend(obQueryExchange(func(_ context.Context, cmd string, body []byte) ([]byte, error) {
		switch cmd {
		case protocol.ReadedReportCommand:
			f, _ := protocol.EveryField(body)
			if g := obFields(obBytes(f, 1)); obNumber(g, 1) == 90001 && obNumber(g, 2) == 44 {
				return []byte{0}, nil
			}
			if c := obFields(obBytes(f, 2)); obString(c, 2) == "u_peer" && obNumber(c, 4) == 9 {
				return []byte{0}, nil
			}
			t.Fatalf("unexpected read report %q %x", cmd, body)
		case protocol.GetGroupMsgCommand:
			f, _ := protocol.EveryField(body)
			info := obFields(obBytes(f, 1))
			if obNumber(info, 1) != 90001 || obNumber(info, 2) != 1 || obNumber(info, 3) != 20 {
				t.Fatalf("group window %+v", info)
			}
			text := protocol.Marshal(protocol.BytesField(1, protocol.Marshal(protocol.StringField(1, "hist"))))
			rich := protocol.Marshal(protocol.BytesField(2, text))
			msg := protocol.Marshal(
				protocol.BytesField(1, protocol.Marshal(protocol.VarintField(1, 123), protocol.StringField(2, "u_s"), protocol.BytesField(8, protocol.Marshal(protocol.VarintField(1, 90001))))),
				protocol.BytesField(2, protocol.Marshal(protocol.VarintField(5, 20), protocol.VarintField(6, 1700000000))),
				protocol.BytesField(3, protocol.Marshal(protocol.BytesField(1, rich))),
			)
			return ssoWrap(protocol.Marshal(protocol.BytesField(3, protocol.Marshal(protocol.BytesField(6, msg))))), nil
		default:
			t.Fatal(cmd)
		}
		return nil, nil
	}))
	backend.remember(123, "u_peer", "")
	if _, err := backend.OneBotAction(context.Background(), "mark_group_msg_as_read", json.RawMessage(`{"group_id":90001,"message_seq":44}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.OneBotAction(context.Background(), "mark_private_msg_as_read", json.RawMessage(`{"user_id":123,"message_seq":9,"time":1}`)); err != nil {
		t.Fatal(err)
	}
	out, err := backend.OneBotAction(context.Background(), "get_group_msg_history", json.RawMessage(`{"group_id":90001,"message_seq":20}`))
	if err != nil {
		t.Fatal(err)
	}
	msgs := out.([]*protocol.IncomingMessage)
	if len(msgs) != 1 || msgs[0].Content != "hist" || msgs[0].GroupUin != 90001 {
		t.Fatal(msgs)
	}
}

func TestGroupRemarkEssenceCookiesAndClientKey(t *testing.T) {
	var cmds []string
	backend := NewOneBotQueryBackend(obQueryExchange(func(_ context.Context, cmd string, body []byte) ([]byte, error) {
		cmds = append(cmds, cmd)
		switch cmd {
		case protocol.OidbCommand(0xf16, 1), protocol.OidbCommand(0xeac, 1):
			return ssoWrap(protocol.Marshal(protocol.VarintField(1, 0xf16), protocol.VarintField(3, 0))), nil
		case protocol.FetchCookieCommand:
			return protocol.BuildOIDB(protocol.OIDBRequest{
				Command: protocol.OIDBCommand{Service: 0x102A, SubCommand: 0},
				Body: protocol.Marshal(protocol.BytesField(1, protocol.Marshal(
					protocol.StringField(1, "qun.qq.com"), protocol.StringField(2, "pskey"),
				))),
			}), nil
		case protocol.FetchClientKeyCommand:
			return protocol.BuildOIDB(protocol.OIDBRequest{
				Command: protocol.OIDBCommand{Service: 0x102A, SubCommand: 1},
				Body:    protocol.Marshal(protocol.StringField(3, "ckey"), protocol.VarintField(4, 1)),
			}), nil
		case protocol.OidbCommand(0x8a7, 0):
			return ssoWrap(protocol.Marshal(
				protocol.VarintField(1, 0x8a7), protocol.VarintField(3, 0),
				protocol.BytesField(4, protocol.Marshal(protocol.VarintField(1, 1), protocol.VarintField(2, 2), protocol.VarintField(3, 5))),
			)), nil
		case protocol.OidbCommand(0xbcb, 0):
			return ssoWrap(protocol.Marshal(
				protocol.VarintField(1, 0xbcb), protocol.VarintField(3, 0),
				protocol.BytesField(4, protocol.Marshal(protocol.BytesField(10, protocol.Marshal(protocol.BytesField(1, protocol.Marshal(protocol.VarintField(7, 2))))))),
			)), nil
		case protocol.OidbCommand(0xeb7, 1):
			return ssoWrap(protocol.Marshal(
				protocol.VarintField(1, 0xeb7), protocol.VarintField(3, 0),
				protocol.BytesField(4, protocol.Marshal(protocol.BytesField(2, protocol.Marshal(protocol.BytesField(2, protocol.Marshal(
					protocol.StringField(1, "ok"), protocol.StringField(2, "1d"),
				)))))),
			)), nil
		default:
			t.Fatal(cmd)
			return nil, nil
		}
	}))
	backend.credentials.Skey = "@skey"
	backend.credentials.SessionKey = make([]byte, protocol.SessionKeySize)
	backend.online = true
	backend.account.Uin = 10001
	backend.options.Device.PTVersion = "7.0.2"
	if _, err := backend.OneBotAction(context.Background(), "set_group_remark", json.RawMessage(`{"group_id":90001,"remark":"lab"}`)); err != nil {
		t.Fatal(err)
	}
	if cmds[0] != "OidbSvcTrpcTcp.0xf16_1" {
		t.Fatal(cmds[0])
	}
	if err := backend.SetEssenceMessage(context.Background(), 90001, 12, 99, true); err != nil {
		t.Fatal(err)
	}
	cookies, err := backend.OneBotAction(context.Background(), "get_cookies", json.RawMessage(`{"domain":"qun.qq.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	if c := cookies.(map[string]any)["cookies"].(string); !bytes.Contains([]byte(c), []byte("p_skey=")) {
		t.Fatal(c)
	}
	key, err := backend.OneBotAction(context.Background(), "get_clientkey", json.RawMessage(`{}`))
	if err != nil || key.(map[string]any)["clientkey"] != "ckey" {
		t.Fatal(key, err)
	}
	remain, err := backend.OneBotAction(context.Background(), "get_group_at_all_remain", json.RawMessage(`{"group_id":90001}`))
	if err != nil {
		t.Fatal(err)
	}
	info := remain.(map[string]any)
	if info["can_at_all"] != true || info["remain_at_all_count_for_uin"] != uint32(2) {
		t.Fatal(info)
	}
	safe, err := backend.OneBotAction(context.Background(), "check_url_safely", json.RawMessage(`{"url":"https://example.com"}`))
	if err != nil || safe.(map[string]any)["level"] != 1 {
		t.Fatal(safe, err)
	}
	sign, err := backend.OneBotAction(context.Background(), "send_group_sign", json.RawMessage(`{"group_id":90001}`))
	if err != nil || sign.(map[string]any)["title"] != "ok" {
		t.Fatal(sign, err)
	}
}

func TestRKeyOCRGroupFilesAndShutList(t *testing.T) {
	backend := NewOneBotQueryBackend(obQueryExchange(func(_ context.Context, cmd string, _ []byte) ([]byte, error) {
		switch cmd {
		case protocol.OidbCommand(0x9067, 202):
			return ssoWrap(protocol.Marshal(
				protocol.VarintField(1, 0x9067), protocol.VarintField(3, 0),
				protocol.BytesField(4, protocol.Marshal(protocol.BytesField(4, protocol.Marshal(
					protocol.BytesField(1, protocol.Marshal(protocol.StringField(1, "&rkey=x"), protocol.VarintField(2, 9), protocol.VarintField(5, 10))),
				)))),
			)), nil
		case protocol.OidbCommand(0xe07, 0):
			return ssoWrap(protocol.Marshal(
				protocol.VarintField(1, 0xe07), protocol.VarintField(3, 0),
				protocol.BytesField(4, protocol.Marshal(protocol.VarintField(1, 0), protocol.BytesField(10, protocol.Marshal(
					protocol.BytesField(1, protocol.Marshal(protocol.StringField(1, "hi"))), protocol.StringField(2, "zh"),
				)))),
			)), nil
		case protocol.OidbCommand(0x88d, 0):
			return ssoWrap(protocol.Marshal(
				protocol.VarintField(1, 0x88d), protocol.VarintField(3, 0),
				protocol.BytesField(4, protocol.Marshal(protocol.BytesField(1, protocol.Marshal(
					protocol.VarintField(1, 90001), protocol.VarintField(2, 0),
					protocol.BytesField(3, protocol.Marshal(protocol.StringField(1, "123"), protocol.StringField(15, "lab"), protocol.VarintField(6, 4))),
				)))),
			)), nil
		case protocol.OidbCommand(0x6d8, 1):
			file := protocol.Marshal(protocol.StringField(1, "fid"), protocol.StringField(2, "a.txt"), protocol.VarintField(3, 12))
			return ssoWrap(protocol.Marshal(
				protocol.VarintField(1, 0x6d8), protocol.VarintField(3, 0),
				protocol.BytesField(4, protocol.Marshal(protocol.BytesField(2, protocol.Marshal(
					protocol.VarintField(1, 0), protocol.VarintField(4, 1),
					protocol.BytesField(5, protocol.Marshal(protocol.VarintField(1, 2), protocol.BytesField(3, file))),
				)))),
			)), nil
		case protocol.OidbCommand(0x6d8, 2):
			return ssoWrap(protocol.Marshal(
				protocol.VarintField(1, 0x6d8), protocol.VarintField(3, 0),
				protocol.BytesField(4, protocol.Marshal(protocol.BytesField(3, protocol.Marshal(protocol.VarintField(4, 2), protocol.VarintField(6, 100))))),
			)), nil
		case protocol.OidbCommand(0x6d8, 3):
			return ssoWrap(protocol.Marshal(
				protocol.VarintField(1, 0x6d8), protocol.VarintField(3, 0),
				protocol.BytesField(4, protocol.Marshal(protocol.BytesField(4, protocol.Marshal(protocol.VarintField(4, 99), protocol.VarintField(5, 7))))),
			)), nil
		case protocol.OidbCommand(0xfe7, 3):
			muted := protocol.Marshal(
				protocol.BytesField(1, protocol.Marshal(protocol.StringField(2, "u_m"), protocol.VarintField(4, 123))),
				protocol.StringField(10, "n"), protocol.VarintField(102, uint64(^uint32(0))), protocol.VarintField(107, 1),
			)
			free := protocol.Marshal(
				protocol.BytesField(1, protocol.Marshal(protocol.StringField(2, "u_f"), protocol.VarintField(4, 456))),
				protocol.StringField(10, "f"), protocol.VarintField(102, 1), protocol.VarintField(107, 1),
			)
			return obFixtureReply(cmd, protocol.Marshal(protocol.VarintField(1, 90001), protocol.BytesField(2, muted), protocol.BytesField(2, free))), nil
		default:
			t.Fatal(cmd)
			return nil, nil
		}
	}))
	keys, err := backend.OneBotAction(context.Background(), "get_rkey", json.RawMessage(`{}`))
	if err != nil || keys.(map[string]any)["rkeys"].([]map[string]any)[0]["rkey"] != "&rkey=x" {
		t.Fatal(keys, err)
	}
	ocr, err := backend.OneBotAction(context.Background(), "ocr_image", json.RawMessage(`{"image":"https://gchat.qpic.cn/a"}`))
	if err != nil || ocr.(map[string]any)["language"] != "zh" {
		t.Fatal(ocr, err)
	}
	info, err := backend.OneBotAction(context.Background(), "get_group_info_ex", json.RawMessage(`{"group_id":90001}`))
	if err != nil || info.(map[string]any)["group_name"] != "lab" {
		t.Fatal(info, err)
	}
	detail, err := backend.OneBotAction(context.Background(), "get_group_detail_info", json.RawMessage(`{"group_id":90001}`))
	if err != nil || detail.(map[string]any)["group_name"] != "lab" {
		t.Fatal(detail, err)
	}
	root, err := backend.OneBotAction(context.Background(), "get_group_root_files", json.RawMessage(`{"group_id":90001}`))
	if err != nil {
		t.Fatal(err)
	}
	if root.(map[string]any)["files"].([]map[string]any)[0]["file_name"] != "a.txt" {
		t.Fatal(root)
	}
	sys, err := backend.OneBotAction(context.Background(), "get_group_file_system_info", json.RawMessage(`{"group_id":90001}`))
	if err != nil || sys.(map[string]any)["file_count"] != uint32(2) || sys.(map[string]any)["used_space"] != uint64(7) {
		t.Fatal(sys, err)
	}
	shut, err := backend.OneBotAction(context.Background(), "get_group_shut_list", json.RawMessage(`{"group_id":90001}`))
	if err != nil {
		t.Fatal(err)
	}
	list := shut.([]map[string]any)
	if len(list) != 1 || list[0]["user_id"] != uint32(123) {
		t.Fatal(list)
	}
}

func TestGroupFileMoveRenamePrivateAndDownload(t *testing.T) {
	var cmds []string
	backend := NewOneBotQueryBackend(obQueryExchange(func(_ context.Context, cmd string, _ []byte) ([]byte, error) {
		cmds = append(cmds, cmd)
		switch cmd {
		case protocol.OidbCommand(0x6d6, 4):
			return ssoWrap(protocol.Marshal(
				protocol.VarintField(1, 0x6d6), protocol.VarintField(3, 0),
				protocol.BytesField(4, protocol.Marshal(protocol.BytesField(5, protocol.Marshal(protocol.VarintField(1, 0))))),
			)), nil
		case protocol.OidbCommand(0x6d6, 5):
			return ssoWrap(protocol.Marshal(
				protocol.VarintField(1, 0x6d6), protocol.VarintField(3, 0),
				protocol.BytesField(4, protocol.Marshal(protocol.BytesField(6, protocol.Marshal(protocol.VarintField(1, 0))))),
			)), nil
		case protocol.OidbCommand(0x6d7, 2):
			return ssoWrap(protocol.Marshal(
				protocol.VarintField(1, 0x6d7), protocol.VarintField(3, 0),
				protocol.BytesField(4, protocol.Marshal(protocol.BytesField(3, protocol.Marshal(protocol.VarintField(1, 0))))),
			)), nil
		case protocol.OidbCommand(0xe37, 1200):
			return ssoWrap(protocol.Marshal(
				protocol.VarintField(1, 0xe37), protocol.VarintField(3, 0),
				protocol.BytesField(4, protocol.Marshal(protocol.BytesField(14, protocol.Marshal(
					protocol.BytesField(30, protocol.Marshal(
						protocol.StringField(20, "10.0.0.1"), protocol.VarintField(40, 80), protocol.StringField(50, "/dl?x=1"),
					)),
				)))),
			)), nil
		case protocol.OidbCommand(0x9082, 1):
			return ssoWrap(protocol.Marshal(protocol.VarintField(1, 0x9082), protocol.VarintField(3, 0))), nil
		default:
			t.Fatal(cmd)
			return nil, nil
		}
	}))
	backend.credentials.Uid = "u_self"
	if _, err := backend.OneBotAction(context.Background(), "rename_group_file", json.RawMessage(`{"group_id":90001,"file_id":"fid","name":"b.txt"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.OneBotAction(context.Background(), "move_group_file", json.RawMessage(`{"group_id":90001,"file_id":"fid","folder_id":"/docs"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.OneBotAction(context.Background(), "set_group_file_folder_name", json.RawMessage(`{"group_id":90001,"folder_id":"/f","name":"docs"}`)); err != nil {
		t.Fatal(err)
	}
	got, err := backend.OneBotAction(context.Background(), "get_private_file_url", json.RawMessage(`{"file_id":"uuid-1","file_hash":"hash-1"}`))
	if err != nil || got.(map[string]any)["url"] != "http://10.0.0.1:80/dl?x=1&isthumb=0" {
		t.Fatal(got, err)
	}
	if err := backend.SetGroupReaction(context.Background(), 90001, 12, "76", true); err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 5 || cmds[0] != protocol.OidbCommand(0x6d6, 4) || cmds[1] != protocol.OidbCommand(0x6d6, 5) || cmds[4] != protocol.OidbCommand(0x9082, 1) {
		t.Fatal(cmds)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello-file"))
	}))
	defer srv.Close()
	dl, err := backend.OneBotAction(context.Background(), "download_file", json.RawMessage(`{"url":"`+srv.URL+`/a.bin","name":"a.bin"}`))
	if err != nil {
		t.Fatal(err)
	}
	out := dl.(map[string]any)
	if out["file_name"] != "a.bin" || out["file_size"] != int64(10) {
		t.Fatal(out)
	}
}

func TestTransGroupFilePacketStatusAndOnlineClients(t *testing.T) {
	var sendBody []byte
	backend := NewOneBotQueryBackend(obQueryExchange(func(_ context.Context, cmd string, body []byte) ([]byte, error) {
		if cmd == protocol.OidbCommand(0x6d9, 4) {
			sendBody = append([]byte(nil), body...)
			return ssoWrap(protocol.Marshal(protocol.VarintField(1, 0x6d9), protocol.VarintField(3, 0))), nil
		}
		t.Fatal(cmd)
		return nil, nil
	}))
	backend.account.AppID = 537164989
	backend.options.Device.DeviceName = "lab-mac"
	backend.options.Device.OS = "mac"
	if _, err := backend.OneBotAction(context.Background(), "trans_group_file", json.RawMessage(`{"group_id":90001,"file_id":"fid"}`)); err != nil {
		t.Fatal(err)
	}
	req, err := protocol.UnwrapOIDB(sendBody)
	if err != nil {
		t.Fatal(err)
	}
	inner, _ := protocol.EveryField(obBytes(obFields(req.Body), 5))
	info, _ := protocol.EveryField(obBytes(inner, 3))
	if obNumber(inner, 1) != 90001 || obNumber(inner, 2) != 4 || obString(info, 2) != "fid" {
		t.Fatalf("%+v %+v", inner, info)
	}
	st, err := backend.OneBotAction(context.Background(), "nc_get_packet_status", json.RawMessage(`{}`))
	if err != nil || st != true {
		t.Fatal(st, err)
	}
	clients, err := backend.OneBotAction(context.Background(), "get_online_clients", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	list := clients.(map[string]any)["clients"].([]map[string]any)
	if len(list) != 1 || list[0]["app_id"] != uint32(537164989) || list[0]["device_name"] != "lab-mac" || list[0]["device_kind"] != "Mac" {
		t.Fatal(clients)
	}
}
