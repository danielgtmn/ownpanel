package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Pull requests and their comments (#116): the panel reads a pull request
// to see whether it is still open and whether its repository is private,
// and keeps one comment of its own on it with the address of the preview.
// All of these need the pull_requests permission (read for GET, write for
// the rest), narrowed to one repository.

// Pull request states.
const (
	PullOpen   = "open"
	PullClosed = "closed"
)

// PullRequest is the part of a pull request the panel uses.
type PullRequest struct {
	Number  int
	State   string // PullOpen or PullClosed
	Merged  bool
	HeadSHA string
	// Private: the repository the pull request belongs to is private.
	Private bool
}

type wirePull struct {
	Number json.Number `json:"number"`
	State  string      `json:"state"`
	Merged bool        `json:"merged"`
	Head   struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Repo *struct {
			Private bool `json:"private"`
		} `json:"repo"`
	} `json:"base"`
}

// MaxCommentBytes bounds the body of a comment the panel writes (GitHub
// allows 65536 characters).
const MaxCommentBytes = 32 << 10

// IssueComment is a comment on a pull request (an issue, to GitHub's API).
// Body is untrusted text.
type IssueComment struct {
	ID int64
	// UserLogin and UserType are of its author ("Bot" for Apps).
	UserLogin string
	UserType  string
	// AppID is the App it was written through, 0 if none.
	AppID int64
	Body  string
}

type wireComment struct {
	ID   json.Number `json:"id"`
	Body string      `json:"body"`
	User *struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	PerformedViaApp *struct {
		ID json.Number `json:"id"`
	} `json:"performed_via_github_app"`
}

func (w wireComment) comment() (IssueComment, error) {
	id, err := w.ID.Int64()
	if err != nil || id <= 0 {
		return IssueComment{}, badAnswer("a comment without a valid id")
	}
	c := IssueComment{ID: id, Body: w.Body}
	if w.User != nil {
		c.UserLogin, c.UserType = w.User.Login, w.User.Type
	}
	if w.PerformedViaApp != nil {
		c.AppID, _ = w.PerformedViaApp.ID.Int64()
	}
	return c, nil
}

func pullPath(owner, name string, number int) (string, error) {
	base, err := repoPath(owner, name)
	if err != nil {
		return "", err
	}
	if number <= 0 {
		return "", &Error{Kind: ErrInvalid, Message: "not a pull request number"}
	}
	return base, nil
}

// GetPullRequest returns a pull request. The token must be allowed to read
// pull requests of the repository.
func (c *Client) GetPullRequest(ctx context.Context, token Token, owner, name string, number int) (PullRequest, error) {
	if token.value == "" {
		return PullRequest{}, &Error{Kind: ErrUnauthorized}
	}
	base, err := pullPath(owner, name, number)
	if err != nil {
		return PullRequest{}, err
	}
	var w wirePull
	if _, err := c.do(ctx, http.MethodGet, base+"/pulls/"+strconv.Itoa(number), nil, "Bearer "+token.value, nil, &w, http.StatusOK); err != nil {
		return PullRequest{}, err
	}
	n, err := w.Number.Int64()
	if err != nil || n != int64(number) || (w.State != PullOpen && w.State != PullClosed) || w.Base.Repo == nil {
		return PullRequest{}, badAnswer("the pull request is not what was asked for")
	}
	p := PullRequest{Number: number, State: w.State, Merged: w.Merged, Private: w.Base.Repo.Private}
	if validSHA(w.Head.SHA) {
		p.HeadSHA = w.Head.SHA
	}
	return p, nil
}

// maxCommentPages bounds how many pages of comments are read when looking
// for the panel's own comment.
const maxCommentPages = 10

// IssueComments lists the comments of a pull request, oldest first, up to
// a few thousand. The token must be allowed to read pull requests.
func (c *Client) IssueComments(ctx context.Context, token Token, owner, name string, number int) ([]IssueComment, error) {
	if token.value == "" {
		return nil, &Error{Kind: ErrUnauthorized}
	}
	base, err := pullPath(owner, name, number)
	if err != nil {
		return nil, err
	}
	var out []IssueComment
	for page := 1; page <= min(c.maxPages, maxCommentPages); page++ {
		q := url.Values{"per_page": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)}}
		var list []wireComment
		if _, err := c.do(ctx, http.MethodGet, base+"/issues/"+strconv.Itoa(number)+"/comments", q, "Bearer "+token.value, nil, &list, http.StatusOK); err != nil {
			return nil, err
		}
		for _, w := range list {
			cm, err := w.comment()
			if err != nil {
				return nil, err
			}
			out = append(out, cm)
		}
		if len(list) < perPage {
			break
		}
	}
	return out, nil
}

func checkCommentBody(body string) error {
	if body == "" || len(body) > MaxCommentBytes || !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
		return &Error{Kind: ErrInvalid, Message: "not a valid comment"}
	}
	return nil
}

// CreateIssueComment writes a comment on a pull request and returns its
// id. The token must be allowed to write pull requests.
func (c *Client) CreateIssueComment(ctx context.Context, token Token, owner, name string, number int, body string) (int64, error) {
	if token.value == "" {
		return 0, &Error{Kind: ErrUnauthorized}
	}
	base, err := pullPath(owner, name, number)
	if err != nil {
		return 0, err
	}
	if err := checkCommentBody(body); err != nil {
		return 0, err
	}
	var w wireComment
	if _, err := c.do(ctx, http.MethodPost, base+"/issues/"+strconv.Itoa(number)+"/comments", nil, "Bearer "+token.value,
		map[string]string{"body": body}, &w, http.StatusCreated); err != nil {
		return 0, err
	}
	cm, err := w.comment()
	if err != nil {
		return 0, err
	}
	return cm.ID, nil
}

// UpdateIssueComment replaces the body of a comment. The token must be
// allowed to write pull requests.
func (c *Client) UpdateIssueComment(ctx context.Context, token Token, owner, name string, id int64, body string) error {
	if token.value == "" {
		return &Error{Kind: ErrUnauthorized}
	}
	base, err := repoPath(owner, name)
	if err != nil {
		return err
	}
	if id <= 0 {
		return &Error{Kind: ErrInvalid, Message: "not a comment id"}
	}
	if err := checkCommentBody(body); err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPatch, base+"/issues/comments/"+fmt.Sprint(id), nil, "Bearer "+token.value,
		map[string]string{"body": body}, nil, http.StatusOK)
	return err
}
