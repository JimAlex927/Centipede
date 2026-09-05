package http

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

// preparePublicPageContent mirrors Docmost's public-share sanitization. Page
// content is stored as ProseMirror JSON, so doing this as a JSON tree walk
// keeps the Go path independent from an editor runtime while covering every
// attachment node (image, PDF, video, audio, and custom embeds).
func (handler *Handler) preparePublicPageContent(content json.RawMessage, pageID, workspaceID string) (json.RawMessage, error) {
	if len(content) == 0 || string(content) == "null" {
		return content, nil
	}
	var document any
	if err := json.Unmarshal(content, &document); err != nil {
		return nil, err
	}
	tokens := make(map[string]string)
	collectPublicAttachmentTokens(document, func(attachmentID string) (string, error) {
		if token, ok := tokens[attachmentID]; ok {
			return token, nil
		}
		token, err := handler.tokens.issue(tokenClaims{
			AttachmentID: attachmentID,
			PageID:       pageID,
			WorkspaceID:  workspaceID,
			Type:         "attachment",
		}, time.Hour)
		if err == nil {
			tokens[attachmentID] = token
		}
		return token, err
	})
	result, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func collectPublicAttachmentTokens(value any, tokenFor func(string) (string, error)) {
	switch node := value.(type) {
	case map[string]any:
		if nodeType, ok := node["type"].(string); ok && isPublicAttachmentNode(nodeType) {
			if attrs, ok := node["attrs"].(map[string]any); ok {
				if attachmentID, ok := attrs["attachmentId"].(string); ok && attachmentID != "" {
					if token, err := tokenFor(attachmentID); err == nil {
						for _, key := range []string{"src", "url"} {
							if raw, ok := attrs[key].(string); ok {
								attrs[key] = publicAttachmentURL(raw, token)
							}
						}
					}
				}
			}
		}
		if marks, ok := node["marks"].([]any); ok {
			filtered := marks[:0]
			for _, mark := range marks {
				markMap, ok := mark.(map[string]any)
				if !ok || markMap["type"] != "comment" {
					filtered = append(filtered, mark)
				}
			}
			node["marks"] = filtered
		}
		for _, child := range node {
			collectPublicAttachmentTokens(child, tokenFor)
		}
	case []any:
		for _, child := range node {
			collectPublicAttachmentTokens(child, tokenFor)
		}
	}
}

func isPublicAttachmentNode(nodeType string) bool {
	switch nodeType {
	case "attachment", "image", "video", "audio", "pdf", "excalidraw", "drawio":
		return true
	default:
		return false
	}
}

func publicAttachmentURL(raw, token string) string {
	if raw == "" || token == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	switch {
	case strings.HasPrefix(parsed.Path, "/api/files/"):
		parsed.Path = "/api/files/public/" + strings.TrimPrefix(parsed.Path, "/api/files/")
	case strings.HasPrefix(parsed.Path, "/files/"):
		parsed.Path = "/files/public/" + strings.TrimPrefix(parsed.Path, "/files/")
	default:
		return raw
	}
	query := parsed.Query()
	query.Set("jwt", token)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
