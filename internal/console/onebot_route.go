package console

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"openopq/internal/api"
)

// OneBot has one self_id per connection. Multiple accounts use a dedicated port
// or /accounts/<session>; self_id/qq are explicit shared-port extensions.
func (h *Hub) routeOneBot(w http.ResponseWriter, r *http.Request, id *string) bool {
	if !api.IsOneBotPath(r.URL.Path) {
		return false
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		failure(w, 400, e)
		return true
	}
	if len(q["session"]) > 1 {
		failure(w, 400, fmt.Errorf("duplicate session selector"))
		return true
	}
	selector := ""
	for _, key := range []string{"self_id", "qq"} {
		values, present := q[key]
		if !present {
			continue
		}
		if len(values) != 1 {
			failure(w, 400, fmt.Errorf("duplicate account selector"))
			return true
		}
		uin, err := strconv.ParseUint(values[0], 10, 32)
		if err != nil || uin == 0 || fmt.Sprint(uin) != values[0] {
			failure(w, 400, fmt.Errorf("invalid account selector"))
			return true
		}
		if selector != "" && selector != values[0] {
			failure(w, 400, fmt.Errorf("conflicting account selectors"))
			return true
		}
		selector = values[0]
	}
	h.mu.Lock()
	slots := append([]*slot{}, h.slots...)
	h.mu.Unlock()
	candidates := []*slot{}
	for _, slot := range slots {
		account := slot.account
		if slot.server != nil {
			account = slot.server.Snapshot().Account
		}
		if selector != "" && account != selector {
			continue
		}
		if selector == "" && account == "" {
			continue
		}
		candidates = append(candidates, slot)
	}
	if *id != "" {
		s := h.find(*id)
		if s == nil {
			failure(w, 404, fmt.Errorf("unknown account session"))
			return true
		}
		if selector != "" && (len(candidates) != 1 || candidates[0] != s) {
			failure(w, 400, fmt.Errorf("selector does not match account session"))
			return true
		}
		return false
	}
	if len(candidates) == 1 {
		*id = candidates[0].id
		return false
	}
	if len(candidates) == 0 && selector == "" {
		return false
	}
	failure(w, 400, fmt.Errorf("select one account using its dedicated port, /accounts/<session> path or self_id; unknown/ambiguous selector"))
	return true
}
