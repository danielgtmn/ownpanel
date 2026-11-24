package main

import "net/http"

// compose routes /api/ to the control API and /mcp to the MCP server (both
// authenticate every request) and everything else to the web interface. The interface is public
// on purpose: the app itself asks /api/session whether someone is signed in.
func compose(apiHandler, web, mcpHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/", web)
	return mux
}
