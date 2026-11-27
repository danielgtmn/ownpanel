package api

import (
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/panel"
)

// GitHub's deliveries (panel/github_webhook.go has the whole order of
// checks). This route is served outside authenticate: it is reached
// without a session or token, so no cookie, Authorization header, Origin
// or CSRF header is read or needed, and nothing else changes for the
// other routes. Only POST on exactly panel.GitHubWebhookPath gets here.

// MaxDeliveryBytes caps a delivery's body. GitHub sends up to 25 MB, but
// what the panel reads (a push, an installation change) is far smaller; a
// push with very many commits that is larger is refused and creates no
// run.
const MaxDeliveryBytes = 5 << 20

// maxDeliveriesAtOnce bounds the deliveries being read at the same time,
// and with it the memory they take.
const maxDeliveriesAtOnce = 8

var deliverySlots = make(chan struct{}, maxDeliveriesAtOnce)

// webhookRoutes registers the routes that are not authenticated.
func (s *Server) webhookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST "+panel.GitHubWebhookPath, s.githubDelivery)
	mux.HandleFunc("POST "+panel.GitNotifyPath+"{id}", s.gitNotification)
}

// rejectDelivery is the one answer to a delivery that is not accepted, so
// it tells nothing about which Apps or repositories exist.
func rejectDelivery(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, panel.ErrDeliveryRejected.Error())
}

func (s *Server) githubDelivery(w http.ResponseWriter, r *http.Request) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		rejectDelivery(w)
		return
	}
	if r.ContentLength > MaxDeliveryBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "the delivery is too large")
		return
	}
	if !s.Panel.AllowGitHubDelivery() {
		writeError(w, http.StatusTooManyRequests, "too many deliveries; try again later")
		return
	}
	select {
	case deliverySlots <- struct{}{}:
		defer func() { <-deliverySlots }()
	default:
		writeError(w, http.StatusTooManyRequests, "too many deliveries; try again later")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxDeliveryBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "the delivery is too large")
			return
		}
		rejectDelivery(w)
		return
	}
	res, err := s.Panel.ReceiveGitHubDelivery(r.Context(), panel.GitHubDelivery{
		ID:         r.Header.Get(github.HeaderDelivery),
		Event:      r.Header.Get(github.HeaderEvent),
		TargetType: r.Header.Get(github.HeaderTargetType),
		TargetID:   r.Header.Get(github.HeaderTargetID),
		Signature:  r.Header.Get(github.HeaderSignature),
		Body:       body,
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
