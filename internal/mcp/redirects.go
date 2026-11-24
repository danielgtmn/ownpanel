package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

type redirectInfo struct {
	ID           int64  `json:"id" jsonschema:"redirect id; pass it to set_redirect to change the redirect or to delete_redirect"`
	Owner        string `json:"owner" jsonschema:"user the redirect belongs to (for path redirects: the site's owner)"`
	SourceDomain string `json:"source_domain" jsonschema:"domain the redirect applies to"`
	SourcePath   string `json:"source_path" jsonschema:"empty for a whole-domain redirect; otherwise an exact path like /old or a prefix like /blog/*"`
	Target       string `json:"target" jsonschema:"where visitors are sent: a full address, or for path redirects a path on the same domain"`
	Code         int    `json:"code" jsonschema:"HTTP status code: 301 or 308 permanent, 302 or 307 temporary"`
	KeepPath     bool   `json:"keep_path" jsonschema:"whether the requested path (or for a prefix, the part after it) is appended to the target"`
	KeepQuery    bool   `json:"keep_query" jsonschema:"whether the query string is appended to the target"`
	CreatedAt    string `json:"created_at" jsonschema:"when the redirect was created, RFC 3339 (UTC)"`
}

func redirectInfoOf(r panel.Redirect) redirectInfo {
	return redirectInfo{
		ID: r.ID, Owner: r.Owner, SourceDomain: r.SourceDomain, SourcePath: r.SourcePath, Target: r.Target,
		Code: r.Code, KeepPath: r.KeepPath, KeepQuery: r.KeepQuery, CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
	}
}

type listRedirectsInput struct {
	Site string `json:"site,omitempty" jsonschema:"only the path redirects of this site; leave empty for all redirects you can see"`
}

type redirectsOutput struct {
	Redirects []redirectInfo `json:"redirects"`
}

type setRedirectInput struct {
	ID           int64  `json:"id,omitempty" jsonschema:"id of an existing redirect to replace; leave empty (or 0) to create a new redirect"`
	SourceDomain string `json:"source_domain" jsonschema:"domain to redirect from, e.g. www.example.com"`
	SourcePath   string `json:"source_path,omitempty" jsonschema:"leave empty to redirect the whole domain (which must not belong to a site); otherwise an exact path like /old.html or a prefix like /blog/* on a domain of one of your sites"`
	Target       string `json:"target" jsonschema:"where to send visitors: a full address like https://example.com, or for path redirects also a path like /new.html"`
	Code         int    `json:"code,omitempty" jsonschema:"301 (default, permanent), 308 (permanent, keeps the request method), 302 or 307 (temporary)"`
	KeepPath     bool   `json:"keep_path,omitempty" jsonschema:"append the requested path to the target (for a prefix: the part after it), e.g. www.example.com/a -> https://example.com/a"`
	KeepQuery    bool   `json:"keep_query,omitempty" jsonschema:"append the query string (?a=1) to the target"`
}

type redirectOutput struct {
	Redirect redirectInfo `json:"redirect"`
}

type deleteRedirectInput struct {
	ID            int64  `json:"id" jsonschema:"id of the redirect to delete, from list_redirects"`
	ConfirmSource string `json:"confirm_source" jsonschema:"the redirect's source_domain followed directly by its source_path, e.g. www.example.com or example.com/old; confirms which redirect is deleted"`
}

type deleteRedirectOutput struct {
	Deleted string `json:"deleted" jsonschema:"source of the deleted redirect"`
}

// addRedirectTools registers the redirect tools.
func addRedirectTools(s *sdk.Server, p *panel.Service) {
	add(s, &sdk.Tool{
		Name:  "list_redirects",
		Title: "List redirects",
		Description: "List the redirects you can see: whole-domain redirects (source_path empty) and path redirects on the " +
			"domains of your sites, sorted by domain and path. Give a site name to list only that site's path redirects.",
		Annotations: annotations("List redirects", true, false, true),
	}, func(ctx context.Context, c call, in listRedirectsInput) (redirectsOutput, error) {
		var list []panel.Redirect
		var err error
		if in.Site != "" {
			list, err = p.SiteRedirects(ctx, c.actor, in.Site)
		} else {
			list, err = p.Redirects(ctx, c.actor)
		}
		if err != nil {
			return redirectsOutput{}, err
		}
		out := redirectsOutput{Redirects: []redirectInfo{}}
		for _, r := range list {
			out.Redirects = append(out.Redirects, redirectInfoOf(r))
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "set_redirect",
		Title: "Create or change a redirect",
		Description: "Create a redirect, or replace an existing one when id is given. A whole-domain redirect (no source_path) " +
			"answers every request for a domain that no site uses, e.g. www.example.com -> https://example.com with keep_path " +
			"and keep_query. A path redirect answers one path (/old) or everything below a prefix (/blog/*) on a domain of one " +
			"of your sites, before the site's files. The domain must point at this server (check_site_domains) for the " +
			"redirect to be reached. Returns the saved redirect.",
		Annotations: annotations("Create or change a redirect", false, true, false),
	}, func(ctx context.Context, c call, in setRedirectInput) (redirectOutput, error) {
		code := in.Code
		if code == 0 {
			code = 301
		}
		ri := panel.RedirectInput{
			SourceDomain: in.SourceDomain, SourcePath: in.SourcePath, Target: in.Target,
			Code: code, KeepPath: in.KeepPath, KeepQuery: in.KeepQuery,
		}
		var r panel.Redirect
		var err error
		if in.ID > 0 {
			r, err = p.UpdateRedirect(ctx, c.actor, in.ID, ri)
		} else {
			r, err = p.CreateRedirect(ctx, c.actor, ri)
		}
		return redirectOutput{redirectInfoOf(r)}, err
	})

	add(s, &sdk.Tool{
		Name:  "delete_redirect",
		Title: "Delete a redirect",
		Description: "Delete a redirect; requests it answered then reach the site again (path redirects) or nothing " +
			"(whole-domain redirects). Only call it when the user asked for it, and repeat the redirect's source " +
			"(source_domain followed by source_path, as shown by list_redirects) in confirm_source.",
		Annotations: annotations("Delete a redirect", false, true, false),
	}, func(ctx context.Context, c call, in deleteRedirectInput) (deleteRedirectOutput, error) {
		r, err := p.Redirect(ctx, c.actor, in.ID)
		if err != nil {
			return deleteRedirectOutput{}, err
		}
		source := r.SourceDomain + r.SourcePath
		if strings.TrimSpace(in.ConfirmSource) != source {
			return deleteRedirectOutput{}, plainError(fmt.Sprintf("not deleted: confirm_source %q does not match the redirect's source %q; repeat it exactly to delete the redirect", in.ConfirmSource, source))
		}
		if err := p.DeleteRedirect(ctx, c.actor, in.ID); err != nil {
			return deleteRedirectOutput{}, err
		}
		return deleteRedirectOutput{Deleted: source}, nil
	})
}
