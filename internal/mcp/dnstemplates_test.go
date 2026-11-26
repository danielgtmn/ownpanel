package mcp

// The tools of dnstemplates.go and their annotations (see TestToolsAndAnnotations).
var _ = expectTools(map[string]toolWant{
	"list_dns_record_templates": {readOnly: true, idempotent: true},
	"apply_dns_record_template": {destructive: true, idempotent: true},
})
