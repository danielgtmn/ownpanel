package api

import (
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

// Notifications of Git hosts for sources of kind git (#111);
// panel/gitnotify.go has the whole order of checks. Like GitHub's
// deliveries this route is served outside authenticate: no cookie,
// Authorization header, Origin or CSRF header is read or needed. It
// shares the size limit and the slots of GitHub's deliveries.

func (s *Server) gitNotification(w http.ResponseWriter, r *http.Request) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		rejectDelivery(w)
		return
	}
	if r.ContentLength > MaxDeliveryBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "the notification is too large")
		return
	}
	if !s.Panel.AllowGitNotification() {
		writeError(w, http.StatusTooManyRequests, "too many notifications; try again later")
		return
	}
	select {
	case deliverySlots <- struct{}{}:
		defer func() { <-deliverySlots }()
	default:
		writeError(w, http.StatusTooManyRequests, "too many notifications; try again later")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxDeliveryBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "the notification is too large")
			return
		}
		rejectDelivery(w)
		return
	}
	res, err := s.Panel.ReceiveGitNotification(r.Context(), panel.GitNotification{
		ID: r.PathValue("id"), Header: r.Header, Body: body,
	})
	if err != nil {
		if panel.KindOf(err) == panel.Invalid {
			rejectDelivery(w)
			return
		}
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
