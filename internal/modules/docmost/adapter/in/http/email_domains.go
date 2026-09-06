package http

import (
	"regexp"
	"strings"
)

var allowedEmailDomainPattern = regexp.MustCompile(`(?i)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9][a-z0-9-]{0,61}[a-z0-9]`)

func normalizeEmailDomains(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		domain := allowedEmailDomainPattern.FindString(strings.TrimSpace(value))
		if domain == "" {
			continue
		}
		domain = strings.ToLower(domain)
		if _, exists := seen[domain]; exists {
			continue
		}
		seen[domain] = struct{}{}
		result = append(result, domain)
	}
	return result
}

func emailDomainAllowed(email string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	parts := strings.Split(strings.ToLower(strings.TrimSpace(email)), "@")
	if len(parts) != 2 || parts[1] == "" {
		return false
	}
	for _, domain := range allowed {
		if strings.EqualFold(strings.TrimSpace(domain), parts[1]) {
			return true
		}
	}
	return false
}
