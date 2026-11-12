package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"openopq/internal/bot"
)

type queryBot struct {
	*bot.Stub
	self   bot.Self
	groups []bot.Group
	err    error
}

func (b *queryBot) Self(context.Context) (bot.Self, error)      { return b.self, b.err }
func (b *queryBot) Groups(context.Context) ([]bot.Group, error) { return b.groups, b.err }

func TestAccountQueriesUseTheirOwnBot(t *testing.T) {
	for _, value := range []struct {
		uin        uint32
		uid, group string
	}{
		{10001, "u_account_a", "group A"}, {10002, "u_account_b", "group B"},
	} {
		b := &queryBot{Stub: bot.NewStub(), self: bot.Self{Uin: value.uin, UID: value.uid, Online: true}, groups: []bot.Group{{GroupUin: value.uin + 100, GroupName: value.group, MemberCount: 2}}}
		s := NewServer(b, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{OneBot: true})
		r := httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest("POST", "/onebot/v11/get_group_list", nil))
		var groups struct {
			Status string
			Data   []struct {
				ID   uint32 `json:"group_id"`
				Name string `json:"group_name"`
			} `json:"data"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &groups); err != nil {
			t.Fatal(err)
		}
		if groups.Status != "ok" || len(groups.Data) != 1 || groups.Data[0].ID != value.uin+100 || groups.Data[0].Name != value.group {
			t.Fatalf("wrong account groups: %s", r.Body.String())
		}
		r = httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest("GET", "/v1/clusterinfo", nil))
		var self struct {
			Data bot.Self `json:"ResponseData"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &self); err != nil {
			t.Fatal(err)
		}
		if self.Data.Uin != value.uin || self.Data.UID != value.uid {
			t.Fatalf("wrong account identity: %s", r.Body.String())
		}
	}
}

func TestGroupListDoesNotHideConnectionErrors(t *testing.T) {
	s := NewServer(&queryBot{Stub: bot.NewStub(), err: bot.ErrNotConnected}, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{OneBot: true})
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest("POST", "/onebot/v11/get_group_list", nil))
	var response obResponse
	if err := json.Unmarshal(r.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "failed" || response.Retcode == 0 {
		t.Fatalf("error hidden: %s", r.Body.String())
	}
}
