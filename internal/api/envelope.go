// Package api serves the HTTP surface callers of OPQBot already use, so existing bots
// and plugin clients keep working against this implementation unchanged.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Ret codes are the ones the existing clients branch on: zero means the CgiBaseResponse
// carries data, anything else means it carries an error message.
const (
	RetOK    = 0
	RetError = -1
)

type baseResponse struct {
	Ret    int    `json:"Ret"`
	ErrMsg string `json:"ErrMsg"`
}

type envelope struct {
	CgiBaseResponse baseResponse `json:"CgiBaseResponse"`
	ResponseData    any          `json:"ResponseData"`
	Data            any          `json:"Data"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		// The value is always a plain envelope, so an encoding failure means the code
		// above is wrong; answering with a fixed body keeps the endpoint well-formed.
		slog.Error("encode response", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"CgiBaseResponse":{"Ret":-1,"ErrMsg":"encode error"},"ResponseData":null,"Data":null}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeBase(w http.ResponseWriter, ret int, message string) {
	writeJSON(w, http.StatusOK, envelope{
		CgiBaseResponse: baseResponse{Ret: ret, ErrMsg: message},
	})
}

func writeData(w http.ResponseWriter, value any) {
	writeJSON(w, http.StatusOK, envelope{
		CgiBaseResponse: baseResponse{Ret: RetOK},
		ResponseData:    value,
	})
}
