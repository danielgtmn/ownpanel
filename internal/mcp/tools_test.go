package mcp

// The tools of tools.go and their annotations (see TestToolsAndAnnotations).
var _ = expectTools(map[string]toolWant{
	"list_sites":         {readOnly: true, idempotent: true},
	"get_site":           {readOnly: true, idempotent: true},
	"create_site":        {},
	"set_site_domains":   {destructive: true, idempotent: true},
	"delete_site":        {destructive: true},
	"make_version_live":  {idempotent: true},
	"publish_files":      {},
	"check_site_domains": {readOnly: true, idempotent: true},
	"create_upload_link": {},
	"build_site":         {},
})
