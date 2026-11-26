package api

import (
	"crypto/sha256"
	"encoding/base64"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/panel"
)

// GitHub connections. Connecting happens in the browser and crosses to
// GitHub and back:
//
//  1. POST /api/github/apps (session + CSRF header) starts it and returns
//     the panel's manifest page;
//  2. that page (GET, same site, so the session cookie is sent) posts the
//     manifest to GitHub; it has its own content security policy that
//     allows posting there and nothing else;
//  3. GitHub sends the browser back to GitHubCreatedPath with a code and
//     the state. That request comes from another site, so the browser
//     leaves out the session cookie (SameSite=Strict). The handler changes
//     nothing: it only checks the shape of the values and answers with a
//     redirect to a fixed address of the web app, the values in its
//     fragment (which reaches neither a server log nor another site);
//  4. the web app sends them with POST /api/github/apps/finish (session +
//     CSRF header); the panel checks that the state belongs to this very
//     sign-in, is unused and fresh, and only then exchanges the code.
//
// Installing goes the same way through GitHubInstalledPath and
// POST /api/github/installations.

func (s *Server) githubRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/github", s.githubConnections)
	mux.HandleFunc("POST /api/github/apps", s.startGitHubApp)
	mux.HandleFunc("GET "+panel.GitHubManifestPath, s.githubManifestPage)
	mux.HandleFunc("GET "+panel.GitHubCreatedPath, githubCreated)
	mux.HandleFunc("POST /api/github/apps/finish", s.finishGitHubApp)
	mux.HandleFunc("POST /api/github/apps/{id}/install", s.startGitHubInstall)
	mux.HandleFunc("DELETE /api/github/apps/{id}", s.deleteGitHubApp)
	mux.HandleFunc("GET "+panel.GitHubInstalledPath, githubInstalled)
	mux.HandleFunc("POST /api/github/installations", s.finishGitHubInstall)
	mux.HandleFunc("GET /api/github/installations/{id}/repositories", s.githubRepositories)
	mux.HandleFunc("POST /api/github/refresh", s.refreshGitHub)
	// GitHub's deliveries to panel.GitHubWebhookPath: webhookRoutes
	// (github_webhook.go), outside authentication.
}

func (s *Server) githubConnections(w http.ResponseWriter, r *http.Request) {
	out, err := s.Panel.GitHubConnections(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) startGitHubApp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Organization string `json:"organization"`
	}
	if !decodeJSON(w, r, &body, `{"organization": "my-org"} ("" for your own account)`) {
		return
	}
	out, err := s.Panel.StartGitHubApp(r.Context(), actorOf(r), body.Organization)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// githubFormScript sends the manifest form at once; the button is there
// for browsers that do not run it. Its hash is the page's only script
// permission.
const githubFormScript = `document.getElementById("f").submit()`

var githubFormScriptHash = func() string {
	sum := sha256.Sum256([]byte(githubFormScript))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

var githubPage = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>{{if .Action}}Continue to GitHub{{else}}Connecting GitHub did not work{{end}}</title>
</head>
<body>
{{if .Action}}<form id="f" method="post" action="{{.Action}}">
<input type="hidden" name="manifest" value="{{.Manifest}}">
<p>Taking you to GitHub to create the app that connects this panel.</p>
<button type="submit">Continue to GitHub</button>
</form>
<script>` + githubFormScript + `</script>
{{else}}<p>{{.Message}}</p>
<p><a href="/#/settings">Back to the settings</a></p>
{{end}}</body>
</html>
`))

// githubManifestPage posts the manifest to GitHub. It is a page, not data,
// so it replaces the API's content security policy with one that allows
// exactly its own script and a form to GitHub.
func (s *Server) githubManifestPage(w http.ResponseWriter, r *http.Request) {
	form, err := s.Panel.GitHubManifestForm(r.Context(), actorOf(r), r.URL.Query().Get("state"))
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	data := struct{ Action, Manifest, Message string }{}
	status := http.StatusOK
	if err != nil {
		h.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		status = http.StatusBadRequest
		switch panel.KindOf(err) {
		case panel.Unauthenticated:
			status, data.Message = http.StatusUnauthorized, "Please sign in to the panel first, then connect GitHub again in the settings."
		case panel.Forbidden:
			status, data.Message = http.StatusForbidden, capitalized(err.Error())+"."
		case panel.Internal:
			status, data.Message = http.StatusInternalServerError, "Something went wrong on the server."
			slog.Error("request failed", "err", err)
		default:
			data.Message = capitalized(err.Error()) + "."
		}
	} else {
		h.Set("Content-Security-Policy", "default-src 'none'; script-src "+githubFormScriptHash+
			"; form-action "+form.Origin+"; base-uri 'none'; frame-ancestors 'none'")
		data.Action, data.Manifest = form.Action, form.Manifest
	}
	w.WriteHeader(status)
	_ = githubPage.Execute(w, data)
}

// backToSettings answers a browser coming back from GitHub with a
// redirect to the settings of the web app, values in the fragment. It
// changes nothing; the web app finishes the step with a POST.
func backToSettings(w http.ResponseWriter, values url.Values) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Location", "/#/settings?"+values.Encode())
	w.WriteHeader(http.StatusSeeOther)
}

// githubState: the shape of a state, before the panel looks at it.
func githubState(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil
}

func githubCreated(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code, state := q.Get("code"), q.Get("state")
	if !github.ValidCode(code) || !githubState(state) {
		backToSettings(w, url.Values{"github": {"failed"}})
		return
	}
	backToSettings(w, url.Values{"github": {"created"}, "code": {code}, "state": {state}})
}

func githubInstalled(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out := url.Values{"github": {"installed"}}
	if action := q.Get("setup_action"); action == "request" {
		// An organisation member asked an owner to install it.
		backToSettings(w, url.Values{"github": {"requested"}})
		return
	}
	if id, err := strconv.ParseInt(q.Get("installation_id"), 10, 64); err == nil && id > 0 {
		out.Set("installation_id", strconv.FormatInt(id, 10))
	}
	if state := q.Get("state"); githubState(state) {
		out.Set("state", state)
	}
	backToSettings(w, out)
}

func (s *Server) finishGitHubApp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if !decodeJSON(w, r, &body, `{"code": "…", "state": "…"} (from GitHub's return to the panel)`) {
		return
	}
	out, err := s.Panel.FinishGitHubApp(r.Context(), actorOf(r), body.Code, body.State)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) startGitHubInstall(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "GitHub connection")
	if !ok {
		return
	}
	out, err := s.Panel.StartGitHubInstall(r.Context(), actorOf(r), id)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) finishGitHubInstall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		InstallationID int64  `json:"installation_id"`
		State          string `json:"state"`
	}
	if !decodeJSON(w, r, &body, `{"installation_id": 123, "state": "…"} (from GitHub's return to the panel)`) {
		return
	}
	out, err := s.Panel.FinishGitHubInstall(r.Context(), actorOf(r), body.InstallationID, body.State)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) githubRepositories(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "GitHub installation")
	if !ok {
		return
	}
	out, err := s.Panel.GitHubRepositories(r.Context(), actorOf(r), id)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) refreshGitHub(w http.ResponseWriter, r *http.Request) {
	out, err := s.Panel.RefreshGitHub(r.Context(), actorOf(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteGitHubApp(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "GitHub connection")
	if !ok {
		return
	}
	out, err := s.Panel.DeleteGitHubApp(r.Context(), actorOf(r), id)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func capitalized(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
