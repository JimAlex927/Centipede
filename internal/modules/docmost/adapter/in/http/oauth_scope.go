package http

import "strings"

// requiredOAuthScope mirrors the explicit @OAuthScope annotations in the
// upstream controllers. Routes without an entry keep their normal session or
// API-key authorization semantics and are not unexpectedly restricted for
// OAuth clients.
func requiredOAuthScope(method, path string) string {
	key := strings.ToUpper(strings.TrimSpace(method)) + " " + strings.TrimSpace(path)
	switch key {
	case "POST /api/users/me",
		"POST /api/workspace/info",
		"POST /api/workspace/members",
		"POST /api/search",
		"POST /api/search/suggest",
		"POST /api/spaces",
		"POST /api/spaces/info",
		"POST /api/pages/info",
		"POST /api/pages/recent",
		"POST /api/pages/sidebar-pages",
		"POST /api/comments",
		"GET /api/files/:fileId/:fileName",
		"POST /api/pages/attachments":
		return "read"
	case "POST /api/spaces/create",
		"POST /api/spaces/update",
		"POST /api/pages/create",
		"POST /api/pages/update",
		"POST /api/pages/move-to-space",
		"POST /api/pages/duplicate",
		"POST /api/pages/move",
		"POST /api/comments/create",
		"POST /api/comments/update":
		return "write"
	default:
		return ""
	}
}

func hasOAuthScope(scopes []string, required string) bool {
	if required == "" {
		return true
	}
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}
