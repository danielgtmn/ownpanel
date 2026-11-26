package github

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Branch is a branch of a repository.
type Branch struct {
	Name string
	// SHA is the commit the branch points at.
	SHA string
}

type wireBranch struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

func validSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}

func (w wireBranch) branch() (Branch, error) {
	if !ValidBranch(w.Name) || !validSHA(w.Commit.SHA) {
		return Branch{}, badAnswer("a branch has an invalid name or commit")
	}
	return Branch{Name: w.Name, SHA: w.Commit.SHA}, nil
}

// repoPath is /repos/{owner}/{name}, checked so nothing else can be
// reached through it.
func repoPath(owner, name string) (string, error) {
	if !ValidLogin(owner) || !repoNamePattern.MatchString(name) || name == "." || name == ".." {
		return "", &Error{Kind: ErrInvalid, Message: "not a repository name"}
	}
	return "/repos/" + owner + "/" + name, nil
}

// GetBranch returns one branch of a repository the token may read
// (contents: read). A branch that does not exist is ErrNotFound.
func (c *Client) GetBranch(ctx context.Context, token Token, owner, name, branch string) (Branch, error) {
	if token.value == "" {
		return Branch{}, &Error{Kind: ErrUnauthorized}
	}
	base, err := repoPath(owner, name)
	if err != nil {
		return Branch{}, err
	}
	if !ValidBranch(branch) {
		return Branch{}, &Error{Kind: ErrInvalid, Message: "not a branch name"}
	}
	// Every part is escaped on its own; GitHub takes the slashes of a
	// branch name as they are.
	parts := strings.Split(branch, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	var w wireBranch
	if _, err := c.do(ctx, http.MethodGet, base+"/branches/"+strings.Join(parts, "/"), nil, "Bearer "+token.value, nil, &w, http.StatusOK); err != nil {
		return Branch{}, err
	}
	b, err := w.branch()
	if err != nil {
		return Branch{}, err
	}
	if b.Name != branch {
		// GitHub answers for a renamed branch with its new name.
		return Branch{}, &Error{Kind: ErrNotFound, Message: "the branch was renamed to " + b.Name}
	}
	return b, nil
}

// Branches lists the branches of a repository, in GitHub's order.
// truncated reports that there were more than the client reads (see
// WithMaxPages).
func (c *Client) Branches(ctx context.Context, token Token, owner, name string) (branches []Branch, truncated bool, err error) {
	if token.value == "" {
		return nil, false, &Error{Kind: ErrUnauthorized}
	}
	base, err := repoPath(owner, name)
	if err != nil {
		return nil, false, err
	}
	out := []Branch{}
	seen := map[string]bool{}
	for page := 1; page <= c.maxPages; page++ {
		var list []wireBranch
		q := url.Values{"per_page": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)}}
		if _, err := c.do(ctx, http.MethodGet, base+"/branches", q, "Bearer "+token.value, nil, &list, http.StatusOK); err != nil {
			return nil, false, err
		}
		for _, w := range list {
			b, err := w.branch()
			if err != nil {
				return nil, false, err
			}
			if !seen[b.Name] {
				seen[b.Name] = true
				out = append(out, b)
			}
		}
		if len(list) < perPage {
			return out, false, nil
		}
	}
	return out, true, nil
}
