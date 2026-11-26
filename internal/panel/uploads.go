package panel

import (
	"context"
	"io"
	"sync"
	"time"
)

// UploadLinkLifetime is how long an upload link can be used.
const UploadLinkLifetime = 15 * time.Minute

// maxOpenUploadLinks per user keeps the in-memory table small.
const maxOpenUploadLinks = 20

// uploadGrant lets whoever knows its id publish one archive to one site, as
// the actor who created it.
type uploadGrant struct {
	actor   Actor
	site    string
	live    bool
	expires time.Time
}

// uploads holds the open one-time upload links. They live in memory only: a
// restart simply invalidates them.
type uploads struct {
	mu     sync.Mutex
	grants map[string]uploadGrant
}

// UploadLink is a one-time permission to upload an archive to a site.
type UploadLink struct {
	ID        string
	Site      string
	Live      bool
	ExpiresAt time.Time
}

// CreateUploadLink creates a one-time upload link for a site the actor may
// publish to. The link is the credential: whoever has it can publish once
// within UploadLinkLifetime, as this actor. With live the new version is
// made live at once, else it is only added (see PublishVersion).
func (s *Service) CreateUploadLink(ctx context.Context, a Actor, name string, live bool) (UploadLink, error) {
	site, err := s.visibleSite(ctx, a, name)
	if err != nil {
		return UploadLink{}, err
	}
	if err := needWrite(a); err != nil {
		return UploadLink{}, err
	}
	now := s.now()
	g := uploadGrant{actor: a, site: site.Name, live: live, expires: now.Add(UploadLinkLifetime)}

	s.uploads.mu.Lock()
	defer s.uploads.mu.Unlock()
	if s.uploads.grants == nil {
		s.uploads.grants = map[string]uploadGrant{}
	}
	open := 0
	for id, o := range s.uploads.grants {
		switch {
		case !now.Before(o.expires):
			delete(s.uploads.grants, id)
		case o.actor.UserID == a.UserID:
			open++
		}
	}
	if open >= maxOpenUploadLinks {
		return UploadLink{}, errorf(Conflict, "there are already %d unused upload links; use or wait for them to expire", open)
	}
	id := randomString(32)
	s.uploads.grants[id] = g
	detail := "valid until " + g.expires.UTC().Format(time.RFC3339)
	if !live {
		detail += ", not made live"
	}
	s.record(ctx, a, "site.upload-link", site.Name, detail)
	return UploadLink{ID: id, Site: site.Name, Live: live, ExpiresAt: g.expires}, nil
}

// PublishWithUploadLink publishes an archive using a link from
// CreateUploadLink. The link is used up by the first attempt, successful or
// not. Unknown, used and expired links all answer NotFound.
func (s *Service) PublishWithUploadLink(ctx context.Context, id string, archive io.Reader) (Publication, error) {
	s.uploads.mu.Lock()
	g, ok := s.uploads.grants[id]
	delete(s.uploads.grants, id)
	s.uploads.mu.Unlock()
	if !ok || !s.now().Before(g.expires) {
		return Publication{}, errorf(NotFound, "this upload link is not valid, has already been used or has expired")
	}
	// The link carries the creator's rights only as long as they still have
	// them: a deleted token or ended session also ends its open links.
	a, err := s.refreshActor(ctx, g.actor)
	if err != nil {
		return Publication{}, errorf(NotFound, "this upload link is not valid, has already been used or has expired")
	}
	return s.PublishVersion(ctx, a, g.site, archive, g.live)
}
