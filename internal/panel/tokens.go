package panel

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/store"
)

// Token is what callers see of an access token; the secret is only shown
// once, in NewToken.
type Token struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Access     string     `json:"access"`
	Sites      []string   `json:"sites"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// NewToken is a freshly created token with its secret.
type NewToken struct {
	Token
	Secret string `json:"token"`
}

func tokenView(t store.Token) Token {
	return Token{ID: t.ID, Name: t.Name, Access: t.Access, Sites: t.Sites, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt}
}

// Tokens lists the actor's own tokens.
func (s *Service) Tokens(ctx context.Context, a Actor) ([]Token, error) {
	if err := needSession(a, "managing access tokens"); err != nil {
		return nil, err
	}
	list, err := s.store.Tokens(ctx, a.UserID)
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]Token, 0, len(list))
	for _, t := range list {
		out = append(out, tokenView(t))
	}
	return out, nil
}

// CreateToken creates an access token for the actor. sites nil means every
// site the user may use; otherwise only those (which the user must see).
func (s *Service) CreateToken(ctx context.Context, a Actor, name, access string, sites []string) (NewToken, error) {
	if err := needSession(a, "managing access tokens"); err != nil {
		return NewToken{}, err
	}
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n == 0 || n > 64 {
		return NewToken{}, errorf(Invalid, "the token name must be 1 to 64 characters long")
	}
	if access != "read" && access != "write" {
		return NewToken{}, errorf(Invalid, "access must be \"read\" or \"write\"")
	}
	var limited []string
	if sites != nil {
		if len(sites) == 0 {
			return NewToken{}, errorf(Invalid, "choose at least one site, or all sites")
		}
		limited = []string{}
		for _, name := range sites {
			if _, err := s.visibleSite(ctx, a, name); err != nil {
				if KindOf(err) == NotFound {
					return NewToken{}, errorf(Invalid, "unknown site %q", name)
				}
				return NewToken{}, err
			}
			if !slices.Contains(limited, name) {
				limited = append(limited, name)
			}
		}
		slices.Sort(limited)
	}
	secret := TokenPrefix + randomString(32)
	t, err := s.store.CreateToken(ctx, store.Token{
		UserID: a.UserID, Name: name, Hash: sha256Hex(secret), Access: access, Sites: limited,
	})
	if err != nil {
		return NewToken{}, storeError(err)
	}
	scope := "all sites"
	if limited != nil {
		scope = "sites " + strings.Join(limited, ", ")
	}
	s.record(ctx, a, "token.create", name, fmt.Sprintf("%s access, %s", access, scope))
	return NewToken{Token: tokenView(t), Secret: secret}, nil
}

// DeleteToken removes one of the actor's own tokens.
func (s *Service) DeleteToken(ctx context.Context, a Actor, id int64) error {
	if err := needSession(a, "managing access tokens"); err != nil {
		return err
	}
	list, err := s.store.Tokens(ctx, a.UserID)
	if err != nil {
		return storeError(err)
	}
	i := slices.IndexFunc(list, func(t store.Token) bool { return t.ID == id })
	if i < 0 {
		return errorf(NotFound, "access token not found")
	}
	if err := s.store.DeleteToken(ctx, a.UserID, id); err != nil {
		return storeError(err)
	}
	s.record(ctx, a, "token.delete", list[i].Name, "")
	return nil
}
