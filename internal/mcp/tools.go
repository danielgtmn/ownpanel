package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

// stampLayout is the timestamp every version ID starts with.
const stampLayout = "20060102T150405.000Z"

// ---- Output types ----

type versionInfo struct {
	ID        string `json:"id" jsonschema:"version id; pass it to make_version_live"`
	CreatedAt string `json:"created_at" jsonschema:"when the version was published, RFC 3339 (UTC)"`
	Live      bool   `json:"live" jsonschema:"whether visitors currently see this version"`
}

type siteInfo struct {
	Name        string        `json:"name" jsonschema:"site name"`
	Domains     []string      `json:"domains" jsonschema:"domains the site is served on"`
	Owner       string        `json:"owner" jsonschema:"user who owns the site"`
	LiveVersion string        `json:"live_version" jsonschema:"id of the live version; empty until something was published"`
	Versions    []versionInfo `json:"versions" jsonschema:"all kept versions, newest first"`
}

type siteOutput struct {
	Site siteInfo `json:"site"`
}

type sitesOutput struct {
	Sites []siteInfo `json:"sites"`
}

func siteView(s panel.Site) siteInfo {
	out := siteInfo{Name: s.Name, Domains: s.Domains, Owner: s.Owner, LiveVersion: s.Current, Versions: []versionInfo{}}
	if out.Domains == nil {
		out.Domains = []string{}
	}
	// The panel lists releases oldest first.
	for i := len(s.Releases) - 1; i >= 0; i-- {
		id := s.Releases[i]
		v := versionInfo{ID: id, Live: id == s.Current}
		if len(id) >= len(stampLayout) {
			if t, err := time.Parse(stampLayout, id[:len(stampLayout)]); err == nil {
				v.CreatedAt = t.UTC().Format(time.RFC3339)
			}
		}
		out.Versions = append(out.Versions, v)
	}
	return out
}

// ---- Registration ----

// add registers a tool whose handler gets the authenticated call. Failures
// of the panel become tool errors (isError, no structured output) the model
// can read; anything unexpected is logged and only reported generically.
func add[In, Out any](s *sdk.Server, t *sdk.Tool, fn func(ctx context.Context, c call, in In) (Out, error)) {
	sdk.AddTool(s, t, func(ctx context.Context, _ *sdk.CallToolRequest, in In) (*sdk.CallToolResult, Out, error) {
		var zero Out
		c, ok := callFrom(ctx)
		if !ok {
			return nil, zero, errors.New("Please sign in.")
		}
		out, err := fn(ctx, c, in)
		if err != nil {
			return nil, zero, readable(t.Name, err)
		}
		return nil, out, nil
	})
}

// plainError is a mistake in the caller's arguments found by this package.
type plainError string

func (e plainError) Error() string { return string(e) }

// readable turns an error into the sentence the model gets to see.
func readable(tool string, err error) error {
	var pe plainError
	switch {
	case errors.As(err, &pe):
		return errors.New(capitalize(pe.Error()))
	case panel.KindOf(err) == panel.Internal:
		slog.Error("mcp tool failed", "tool", tool, "err", err)
		return errors.New("Something went wrong on the server.")
	}
	return errors.New(capitalize(err.Error()))
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func ptr(b bool) *bool { return &b }

// annotations: readOnly tools change nothing; destructive tools remove or
// replace something that cannot simply be undone.
func annotations(title string, readOnly, destructive, idempotent bool) *sdk.ToolAnnotations {
	a := &sdk.ToolAnnotations{Title: title, ReadOnlyHint: readOnly, IdempotentHint: idempotent, OpenWorldHint: ptr(false)}
	if !readOnly {
		a.DestructiveHint = ptr(destructive)
	}
	return a
}

// ---- Tools ----

type noInput struct{}

type siteInput struct {
	Site string `json:"site" jsonschema:"site name as shown by list_sites"`
}

type createSiteInput struct {
	Name    string   `json:"name" jsonschema:"new site name: lowercase letters, digits and hyphens, at most 63 characters; it identifies the site in every other tool and is not a domain"`
	Domains []string `json:"domains" jsonschema:"domains to serve the site on, e.g. example.com and www.example.com; may be empty. Each domain can belong to only one site and must point at this server through DNS"`
}

type setDomainsInput struct {
	Name    string   `json:"name" jsonschema:"site name"`
	Domains []string `json:"domains" jsonschema:"the complete new list of domains; domains not listed are removed from the site"`
}

type deleteSiteInput struct {
	Name        string `json:"name" jsonschema:"name of the site to delete"`
	ConfirmName string `json:"confirm_name" jsonschema:"must be exactly the same as name; confirms that the whole site should be deleted"`
}

type deleteSiteOutput struct {
	Deleted string `json:"deleted" jsonschema:"name of the deleted site"`
}

type makeLiveInput struct {
	Site    string `json:"site" jsonschema:"site name"`
	Version string `json:"version,omitempty" jsonschema:"version id from get_site; leave empty to go back to the version before the live one"`
}

type publishInput struct {
	Site  string `json:"site" jsonschema:"name of an existing site"`
	Files []File `json:"files" jsonschema:"all files of the new version; files of earlier versions are not carried over"`
}

type domainInfo struct {
	Domain string   `json:"domain" jsonschema:"the domain"`
	State  string   `json:"state" jsonschema:"points_here (ready), points_elsewhere (DNS points to another server), no_record (no DNS record yet) or lookup_failed (could not be checked, try again later)"`
	Found  []string `json:"found" jsonschema:"addresses the domain currently points to"`
}

type domainsOutput struct {
	Domains []domainInfo `json:"domains"`
}

type uploadLinkOutput struct {
	URL         string `json:"url" jsonschema:"one-time upload address"`
	CurlCommand string `json:"curl_command" jsonschema:"ready-to-run command; replace site.zip with the path of the archive"`
	ExpiresAt   string `json:"expires_at" jsonschema:"when the link stops working, RFC 3339 (UTC)"`
	MaxSizeMB   int    `json:"max_size_mb" jsonschema:"largest archive accepted"`
}

func newServer(p *panel.Service, version string) *sdk.Server {
	s := sdk.NewServer(
		&sdk.Implementation{Name: "ownpanel", Title: "ownpanel", Version: version},
		&sdk.ServerOptions{Instructions: instructions},
	)

	add(s, &sdk.Tool{
		Name:  "list_sites",
		Title: "List sites",
		Description: "List the web sites you can manage in ownpanel, sorted by name. " +
			"Use it first to see what exists. Returns for each site its name, domains, owner, " +
			"the id of the live version and all kept versions (newest first).",
		Annotations: annotations("List sites", true, false, true),
	}, func(ctx context.Context, c call, _ noInput) (sitesOutput, error) {
		sites, err := p.Sites(ctx, c.actor)
		if err != nil {
			return sitesOutput{}, err
		}
		out := sitesOutput{Sites: []siteInfo{}}
		for _, site := range sites {
			out.Sites = append(out.Sites, siteView(site))
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "get_site",
		Title: "Show a site",
		Description: "Show one site: its name, domains, owner, the id of the live version and all kept versions " +
			"(newest first, with publish time). Use it to find version ids for make_version_live.",
		Annotations: annotations("Show a site", true, false, true),
	}, func(ctx context.Context, c call, in siteInput) (siteOutput, error) {
		site, err := p.Site(ctx, c.actor, in.Site)
		return siteOutput{siteView(site)}, err
	})

	add(s, &sdk.Tool{
		Name:  "create_site",
		Title: "Create a site",
		Description: "Create a new, empty site that you own. The name identifies the site in all other tools; " +
			"the domains are the addresses visitors use. Nothing is served until files are published with " +
			"publish_files or create_upload_link. HTTPS certificates are requested automatically once a domain " +
			"points at this server. Fails if the name or a domain is already taken.",
		Annotations: annotations("Create a site", false, false, false),
	}, func(ctx context.Context, c call, in createSiteInput) (siteOutput, error) {
		if in.Domains == nil {
			in.Domains = []string{}
		}
		// SaveSite also updates existing sites; creating must not.
		if _, err := p.Site(ctx, c.actor, in.Name); err == nil {
			return siteOutput{}, plainError(fmt.Sprintf("the site name %q is already taken; use set_site_domains to change an existing site", in.Name))
		} else if panel.KindOf(err) != panel.NotFound {
			return siteOutput{}, err
		}
		site, err := p.SaveSite(ctx, c.actor, in.Name, in.Domains)
		return siteOutput{siteView(site)}, err
	})

	add(s, &sdk.Tool{
		Name:  "set_site_domains",
		Title: "Set the domains of a site",
		Description: "Replace the whole list of domains of an existing site with the given list. Domains that are " +
			"not listed stop being served. Safe to repeat with the same list. Returns the updated site.",
		Annotations: annotations("Set the domains of a site", false, true, true),
	}, func(ctx context.Context, c call, in setDomainsInput) (siteOutput, error) {
		if in.Domains == nil {
			in.Domains = []string{}
		}
		if _, err := p.Site(ctx, c.actor, in.Name); err != nil {
			return siteOutput{}, err // SaveSite would create a missing site instead
		}
		site, err := p.SaveSite(ctx, c.actor, in.Name, in.Domains)
		return siteOutput{siteView(site)}, err
	})

	add(s, &sdk.Tool{
		Name:  "delete_site",
		Title: "Delete a site",
		Description: "Permanently delete a site: it stops being served and all its files and versions are removed. " +
			"This cannot be undone. Only call it when the user clearly asked for it, and pass the site name " +
			"again as confirm_name.",
		Annotations: annotations("Delete a site", false, true, false),
	}, func(ctx context.Context, c call, in deleteSiteInput) (deleteSiteOutput, error) {
		if in.ConfirmName != in.Name {
			return deleteSiteOutput{}, plainError(fmt.Sprintf("not deleted: confirm_name %q does not match name %q; repeat the exact site name in confirm_name to delete the site", in.ConfirmName, in.Name))
		}
		if err := p.DeleteSite(ctx, c.actor, in.Name); err != nil {
			return deleteSiteOutput{}, err
		}
		return deleteSiteOutput{Deleted: in.Name}, nil
	})

	add(s, &sdk.Tool{
		Name:  "make_version_live",
		Title: "Make a version live",
		Description: "Switch a site to one of its kept versions, for example to undo a bad publish. Without a version " +
			"it goes back to the version before the live one. Takes effect immediately and keeps all versions. " +
			"Safe to repeat. Get version ids from get_site.",
		Annotations: annotations("Make a version live", false, false, true),
	}, func(ctx context.Context, c call, in makeLiveInput) (siteOutput, error) {
		site, err := p.MakeLive(ctx, c.actor, in.Site, in.Version)
		return siteOutput{siteView(site)}, err
	})

	add(s, &sdk.Tool{
		Name:  "publish_files",
		Title: "Publish files",
		Description: "Publish a small static site by giving its files directly. This is the usual way to publish what you " +
			"wrote yourself. The files become a new version that goes live at once; they replace the previous " +
			"version completely, so include every file of the site (at least index.html). Paths are relative to the " +
			"site root. Limits: 2000 files, 10 MB in total after decoding. For larger sites use create_upload_link. " +
			"The site must already exist (create_site). Returns the site with its new live version.",
		Annotations: annotations("Publish files", false, false, false),
	}, func(ctx context.Context, c call, in publishInput) (siteOutput, error) {
		data, err := buildArchive(in.Files)
		if err != nil {
			return siteOutput{}, plainError(err.Error())
		}
		site, err := p.Publish(ctx, c.actor, in.Site, bytes.NewReader(data))
		return siteOutput{siteView(site)}, err
	})

	add(s, &sdk.Tool{
		Name:  "check_site_domains",
		Title: "Check the domains of a site",
		Description: "Check for each domain of a site whether its DNS points at this server, which is needed before visitors " +
			"can reach the site and a HTTPS certificate can be issued. Use it after create_site or set_site_domains, " +
			"or when a site cannot be reached. Returns each domain with its state and the addresses it points to. " +
			"DNS changes can take a while to spread, so check again later when a domain does not point here yet.",
		Annotations: annotations("Check the domains of a site", true, false, true),
	}, func(ctx context.Context, c call, in siteInput) (domainsOutput, error) {
		list, err := p.DomainStatuses(ctx, c.actor, in.Site)
		if err != nil {
			return domainsOutput{}, err
		}
		out := domainsOutput{Domains: []domainInfo{}}
		for _, d := range list {
			found := d.Found
			if found == nil {
				found = []string{}
			}
			out.Domains = append(out.Domains, domainInfo{Domain: d.Domain, State: d.State, Found: found})
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "create_upload_link",
		Title: "Create an upload link",
		Description: "Create a one-time upload link for publishing a large site as a .zip or .tar.gz archive (up to 512 MB). " +
			"Use it when the site is too big for publish_files or already exists as an archive. Returns the URL and a " +
			"ready-to-run curl command: run it where the archive is, with the file path filled in. The link works once, " +
			"for 15 minutes, only for this site, and publishes as you. Treat the URL like a password.",
		Annotations: annotations("Create an upload link", false, false, false),
	}, func(ctx context.Context, c call, in siteInput) (uploadLinkOutput, error) {
		link, err := p.CreateUploadLink(ctx, c.actor, in.Site)
		if err != nil {
			return uploadLinkOutput{}, err
		}
		url := c.baseURL + "/api/uploads/" + link.ID
		return uploadLinkOutput{
			URL:         url,
			CurlCommand: fmt.Sprintf("curl --fail-with-body -X POST --data-binary @site.zip '%s'", url),
			ExpiresAt:   link.ExpiresAt.UTC().Format(time.RFC3339),
			MaxSizeMB:   512,
		}, nil
	})

	return s
}
