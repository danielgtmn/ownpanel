package mcp

// The tools of sources.go and their annotations (see TestToolsAndAnnotations).
var _ = expectTools(map[string]toolWant{
	"get_site_source":                {readOnly: true, idempotent: true},
	"list_site_source_repositories":  {readOnly: true, idempotent: true},
	"list_site_source_branches":      {readOnly: true, idempotent: true},
	"link_site_source":               {destructive: true, idempotent: true},
	"unlink_site_source":             {destructive: true},
	"set_site_pull_request_previews": {idempotent: true},
	"list_site_runs":                 {readOnly: true, idempotent: true},
})
