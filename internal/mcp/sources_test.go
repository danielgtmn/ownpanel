package mcp

// The tools of sources.go and their annotations (see TestToolsAndAnnotations).
var _ = expectTools(map[string]toolWant{
	"get_site_source":               {readOnly: true, idempotent: true},
	"list_site_source_repositories": {readOnly: true, idempotent: true},
	"list_site_source_branches":     {readOnly: true, idempotent: true},
	"link_site_source":              {destructive: true, idempotent: true},
	"unlink_site_source":            {destructive: true},
	"list_site_runs":                {readOnly: true, idempotent: true},
	"inspect_git_repository":        {readOnly: true, idempotent: true},
	"link_git_site_source":          {destructive: true, idempotent: true},
	"check_git_site_source":         {idempotent: true},
})
