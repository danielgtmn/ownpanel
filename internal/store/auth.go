package store

import (
	"context"
	"slices"
	"time"

	"github.com/danielgtmn/ablage"
)

// Token is a personal access token. Only the hash of its secret is kept.
type Token struct {
	ID     string
	UserID string
	Name   string
	Hash   string
	Access string // "read" or "write"
	// Sites limits the token to these sites; nil means every site the user
	// may use.
	Sites      []string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	// ExpiresAt is when the token stops working; nil means never.
	ExpiresAt *time.Time
}

var errTokenNotFound = notFoundf("access token not found")

func tokenOf(r tokenRow) Token {
	t := Token{ID: r.ID, UserID: r.UserID, Name: r.Name, Hash: r.Hash, Access: r.Access,
		CreatedAt: seconds(r.CreatedAt), LastUsedAt: r.LastUsedAt, ExpiresAt: r.ExpiresAt}
	if r.Sites != nil {
		t.Sites = append([]string{}, (*r.Sites)...)
	}
	return t
}

// CreateToken stores a token and returns it with id and creation time.
func (s *Store) CreateToken(ctx context.Context, t Token) (Token, error) {
	row := tokenRow{UserID: t.UserID, Name: t.Name, Hash: t.Hash, Access: t.Access,
		CreatedAt: s.now(), ExpiresAt: secondsPtr(t.ExpiresAt)}
	if t.Sites != nil {
		row.Sites = ptr(append([]string{}, t.Sites...))
	}
	if err := s.r.tokens.Create(ctx, &row); err != nil {
		return Token{}, err
	}
	return tokenOf(row), nil
}

// Tokens lists a user's tokens, newest first.
func (s *Store) Tokens(ctx context.Context, userID string) ([]Token, error) {
	list, err := s.r.tokens.Find(ctx, ablage.Query{
		Where:   ablage.Eq("user_id", userID),
		OrderBy: []ablage.Order{ablage.Desc("created_at"), ablage.Desc("id")},
	})
	if err != nil {
		return nil, err
	}
	out := make([]Token, len(list))
	for i, t := range list {
		out[i] = tokenOf(t)
	}
	return out, nil
}

// TokenByHash returns a token and its user.
func (s *Store) TokenByHash(ctx context.Context, hash string) (Token, User, error) {
	row, err := s.r.tokens.First(ctx, ablage.Query{Where: ablage.Eq("hash", hash)})
	if err != nil {
		return Token{}, User{}, missing(err, errTokenNotFound)
	}
	u, err := s.User(ctx, row.UserID)
	return tokenOf(row), u, err
}

// DeleteToken removes a token of the given user.
func (s *Store) DeleteToken(ctx context.Context, userID, id string) error {
	n, err := s.r.tokens.DeleteWhere(ctx, ablage.And(ablage.Eq("id", id), ablage.Eq("user_id", userID)))
	if err != nil {
		return err
	}
	if n == 0 {
		return errTokenNotFound
	}
	return nil
}

// TouchToken records a use of a token, at most once per minute so reads do
// not turn into a write each.
func (s *Store) TouchToken(ctx context.Context, id string, now time.Time) error {
	_, err := s.r.tokens.UpdateWhere(ctx, ablage.And(
		ablage.Eq("id", id),
		ablage.Or(ablage.Null("last_used_at"), ablage.Lte("last_used_at", seconds(now.Add(-time.Minute)))),
	), ablage.Row{"last_used_at": seconds(now)})
	return err
}

// dropSiteFromTokens removes a site from the site lists of tokens, so a
// later site with the same name is not covered by an old token.
func dropSiteFromTokens(ctx context.Context, r repos, name string) error {
	// JSON fields cannot be searched: read the limited tokens and change
	// those that list the site.
	list, err := r.tokens.Find(ctx, ablage.Query{Where: ablage.NotNull("sites")})
	if err != nil {
		return err
	}
	for _, t := range list {
		sites := *t.Sites
		if i := slices.Index(sites, name); i >= 0 {
			sites = slices.Delete(sites, i, i+1)
			if err := r.tokens.Patch(ctx, t.ID, ablage.Row{"sites": sites}); err != nil {
				return err
			}
		}
	}
	return nil
}
