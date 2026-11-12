package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
)

// maxParamBody bounds a request body; protocol operations carry small JSON objects.
const maxParamBody = 256 * 1024

// requestParams gathers the call arguments. Callers send them as a JSON body, as query
// parameters, or both, so both are accepted and the body wins on conflict.
func requestParams(r *http.Request) json.RawMessage {
	merged := map[string]any{}
	for key, values := range r.URL.Query() {
		if key == "funcname" || key == "qq" || key == "timeout" {
			continue
		}
		if len(values) > 0 {
			merged[key] = values[0]
		}
	}
	if body := readBody(r); body != nil {
		var fromBody map[string]any
		if err := json.Unmarshal(body, &fromBody); err == nil {
			for key, value := range fromBody {
				merged[key] = value
			}
		} else {
			return body
		}
	}
	if len(merged) == 0 {
		return nil
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil
	}
	return encoded
}

func readBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxParamBody+1))
	if err != nil || len(body) == 0 || len(body) > maxParamBody {
		return nil
	}
	return body
}

// queryUin reports the account a call names, if any.
func queryUin(r *http.Request) uint32 {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0
	}
	parsed, err := parseUint(values.Get("qq"))
	if err != nil {
		return 0
	}
	return parsed
}
