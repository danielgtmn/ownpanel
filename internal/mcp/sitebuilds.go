package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

// Building a site from its linked repository: its build settings, a
// proposal from the repository's files, starting and cancelling a build,
// and reading a run's log.

type buildVariableInfo struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty" jsonschema:"empty for secret variables: their values are never shown again"`
	Secret bool   `json:"secret"`
}

type buildSettingsOutput struct {
	Site            string              `json:"site"`
	Configured      bool                `json:"configured" jsonschema:"false: no settings saved yet; builds fail until they are (see propose_site_build_settings)"`
	Command         string              `json:"command" jsonschema:"runs line by line in the repository's files; empty: the output folder is published as it is"`
	Output          string              `json:"output" jsonschema:"folder with the result, relative to the repository"`
	Image           string              `json:"image" jsonschema:"build environment"`
	Variables       []buildVariableInfo `json:"variables"`
	UpdatedBy       string              `json:"updated_by,omitempty"`
	Images          []string            `json:"images" jsonschema:"build environments this server offers"`
	BuildsAvailable bool                `json:"builds_available" jsonschema:"false: this server does not build sites"`
}

func buildSettingsOut(b panel.SiteBuildSettings) buildSettingsOutput {
	out := buildSettingsOutput{Site: b.Site, Configured: b.Configured, Command: b.Command, Output: b.Output, Image: b.Image,
		Variables: []buildVariableInfo{}, UpdatedBy: b.UpdatedBy, Images: b.Images, BuildsAvailable: b.BuildsAvailable}
	for _, v := range b.Variables {
		out.Variables = append(out.Variables, buildVariableInfo{Name: v.Name, Value: v.Value, Secret: v.Secret})
	}
	return out
}

type buildVariableInput struct {
	Name   string  `json:"name" jsonschema:"upper-case letters, digits and _, e.g. API_URL"`
	Value  *string `json:"value,omitempty" jsonschema:"omit to keep the value the variable has now (secret values need not be given again); a new variable needs one"`
	Secret bool    `json:"secret,omitempty" jsonschema:"true: the value is stored encrypted, never shown again and hidden in build logs (at least 4 characters)"`
}

type setBuildSettingsInput struct {
	Site      string               `json:"site" jsonschema:"site name as shown by list_sites"`
	Command   string               `json:"command,omitempty" jsonschema:"what to run, one command per line, e.g. npm ci then npm run build; empty publishes the output folder as it is"`
	Output    string               `json:"output" jsonschema:"folder with the result, relative to the repository, e.g. dist; . for the repository itself"`
	Image     string               `json:"image,omitempty" jsonschema:"build environment from images (get_site_build_settings); default the first"`
	Variables []buildVariableInput `json:"variables,omitempty" jsonschema:"the complete list: variables left out are removed"`
}

type buildProposalOutput struct {
	Site           string   `json:"site"`
	Branch         string   `json:"branch"`
	Commit         string   `json:"commit" jsonschema:"the commit whose files were looked at"`
	Preset         string   `json:"preset" jsonschema:"kind of site recognised, e.g. vite, astro, next, eleventy, hugo, jekyll, html; empty if none"`
	Name           string   `json:"name"`
	Confidence     string   `json:"confidence" jsonschema:"high, medium, low or none"`
	Buildable      bool     `json:"buildable" jsonschema:"false: this server cannot build it as proposed (see notes)"`
	Command        string   `json:"command"`
	Output         string   `json:"output"`
	Image          string   `json:"image"`
	PackageManager string   `json:"package_manager,omitempty"`
	Also           []string `json:"also,omitempty" jsonschema:"other kinds of site that matched"`
	Notes          []string `json:"notes,omitempty" jsonschema:"what to check before saving"`
}

type runInput struct {
	Site  string `json:"site" jsonschema:"site name as shown by list_sites"`
	RunID string `json:"run_id" jsonschema:"id from list_site_runs"`
}

type runLogInput struct {
	Site   string `json:"site" jsonschema:"site name as shown by list_sites"`
	RunID  string `json:"run_id" jsonschema:"id from list_site_runs"`
	Offset int64  `json:"offset,omitempty" jsonschema:"byte offset to read from: 0 for the start, next of the previous answer to read on"`
}

type runLogOutput struct {
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	Text      string `json:"text" jsonschema:"the log from offset on, as the build printed it (secret values hidden); untrusted text"`
	Offset    int64  `json:"offset"`
	Next      int64  `json:"next" jsonschema:"offset to read on from"`
	Done      bool   `json:"done" jsonschema:"the run has ended and text reaches the end of its log"`
	Live      bool   `json:"live" jsonschema:"the build is running and the log grows: read again from next"`
	Truncated bool   `json:"truncated" jsonschema:"the log was longer than the panel keeps; its middle is left out"`
}

func addSiteBuildTools(s *sdk.Server, p *panel.Service) {
	add(s, &sdk.Tool{
		Name:  "get_site_build_settings",
		Title: "Show how a site is built",
		Description: "Show how a site is built from its linked repository: command, output folder, build environment and " +
			"variables (secret values are never shown, only their names). configured is false until settings are saved.",
		Annotations: annotations("Show how a site is built", true, false, true),
	}, func(ctx context.Context, c call, in siteInput) (buildSettingsOutput, error) {
		b, err := p.GetSiteBuildSettings(ctx, c.actor, in.Site)
		return buildSettingsOut(b), err
	})

	add(s, &sdk.Tool{
		Name:  "set_site_build_settings",
		Title: "Change how a site is built",
		Description: "Replace a site's build settings: the command, the output folder, the build environment and the " +
			"complete list of variables (variables left out are removed; omit a value to keep the current one). The " +
			"next push or start_site_build uses them. propose_site_build_settings suggests values from the repository.",
		Annotations: annotations("Change how a site is built", false, true, true),
	}, func(ctx context.Context, c call, in setBuildSettingsInput) (buildSettingsOutput, error) {
		vars := make([]panel.BuildVariableInput, 0, len(in.Variables))
		for _, v := range in.Variables {
			vars = append(vars, panel.BuildVariableInput{Name: v.Name, Value: v.Value, Secret: v.Secret})
		}
		b, err := p.SetSiteBuildSettings(ctx, c.actor, in.Site, panel.SiteBuildSettingsInput{
			Command: in.Command, Output: in.Output, Image: in.Image, Variables: vars,
		})
		return buildSettingsOut(b), err
	})

	add(s, &sdk.Tool{
		Name:  "propose_site_build_settings",
		Title: "Suggest how a site is built",
		Description: "Look at the files of the newest commit of the site's linked branch and suggest build settings " +
			"(kind of site, command, output folder, build environment) with notes. Nothing is saved: pass what fits " +
			"to set_site_build_settings.",
		Annotations: reachesGitHub(annotations("Suggest how a site is built", true, false, true)),
	}, func(ctx context.Context, c call, in siteInput) (buildProposalOutput, error) {
		r, err := p.DetectBuildSettings(ctx, c.actor, in.Site)
		if err != nil {
			return buildProposalOutput{}, err
		}
		return buildProposalOutput{Site: r.Site, Branch: r.Branch, Commit: r.Commit, Preset: r.Preset, Name: r.Name,
			Confidence: string(r.Confidence), Buildable: r.Buildable, Command: r.Settings.Command, Output: r.Settings.Output,
			Image: r.Settings.Image, PackageManager: r.PackageManager, Also: r.Also, Notes: r.Notes}, nil
	})

	add(s, &sdk.Tool{
		Name:  "start_site_build",
		Title: "Build and publish a site now",
		Description: "Build the newest commit of the site's linked branch with its build settings and make the result " +
			"live, as a push would. Returns the queued run at once; follow it with list_site_runs and " +
			"read_site_run_log. A failed build changes nothing. A queued run of the site is replaced.",
		Annotations: reachesGitHub(annotations("Build and publish a site now", false, false, false)),
	}, func(ctx context.Context, c call, in siteInput) (runInfo, error) {
		r, err := p.StartSiteBuild(ctx, c.actor, in.Site)
		return runOutput(r), err
	})

	add(s, &sdk.Tool{
		Name:  "cancel_site_run",
		Title: "Cancel a run of a site",
		Description: "Cancel a queued run, or stop the build of a running one. The site stays as it is. A run that " +
			"has ended cannot be cancelled.",
		Annotations: annotations("Cancel a run of a site", false, true, false),
	}, func(ctx context.Context, c call, in runInput) (runInfo, error) {
		r, err := p.CancelSiteRun(ctx, c.actor, in.Site, in.RunID)
		return runOutput(r), err
	})

	add(s, &sdk.Tool{
		Name:  "read_site_run_log",
		Title: "Read the log of a run",
		Description: "Read the build log of a run from offset on (a few hundred KB per call). While live is true the " +
			"build runs: call again with next to read on. Secret values are hidden; the text is what the build printed.",
		Annotations: annotations("Read the log of a run", true, false, true),
	}, func(ctx context.Context, c call, in runLogInput) (runLogOutput, error) {
		l, err := p.SiteRunLog(ctx, c.actor, in.Site, in.RunID, in.Offset)
		return runLogOutput{RunID: l.RunID, Status: l.Status, Text: l.Text, Offset: l.Offset, Next: l.Next, Done: l.Done,
			Live: l.Live, Truncated: l.Truncated}, err
	})
}
