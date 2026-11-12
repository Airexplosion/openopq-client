package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"openopq/internal/bot"
)

const loginTimeout = 180 * time.Second

// handleQRCode returns the login QR code as a PNG data URL, which is what the original
// serves and what its web page renders directly.
func (s *Server) handleQRCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeBase(w, RetError, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), loginTimeout)
	defer cancel()

	png, err := s.bot.QRCode(ctx)
	if err != nil {
		s.logger.Error("qr code request failed", "error", err)
		writeBase(w, RetError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, envelope{
		CgiBaseResponse: baseResponse{Ret: RetOK},
		ResponseData: map[string]string{
			"Qrcode": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		},
	})
}

// handleLoadSession restores a saved session for one account.
func (s *Server) handleLoadSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBase(w, RetError, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), loginTimeout)
	defer cancel()

	if err := s.bot.LoadSession(ctx); err != nil {
		if errors.Is(err, bot.ErrNotConnected) {
			writeBase(w, RetError, "no saved session")
			return
		}
		s.logger.Error("load session failed", "error", err)
		writeBase(w, RetError, err.Error())
		return
	}
	writeData(w, map[string]any{"LoadSession": true})
}
