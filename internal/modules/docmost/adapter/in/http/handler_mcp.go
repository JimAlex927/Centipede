package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/domain"

	"github.com/gin-gonic/gin"
)

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type mcpTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

func (handler *Handler) mcpEndpoint(c *gin.Context) {
	current := currentPrincipal(c)
	if !handler.workspaceHasFeature(c.Request.Context(), current.Workspace.ID, "mcp") {
		writeError(c, http.StatusForbidden, "MCP requires an active license")
		return
	}
	var settings struct {
		AI struct {
			MCP             bool `json:"mcp"`
			EnforceMCPOAuth bool `json:"enforceMcpOauth"`
		} `json:"ai"`
	}
	if json.Unmarshal(current.Workspace.Settings, &settings) != nil || !settings.AI.MCP {
		writeError(c, http.StatusNotFound, "MCP is not enabled for this workspace")
		return
	}
	if settings.AI.EnforceMCPOAuth && current.OAuthGrantID == "" {
		c.Header("WWW-Authenticate", `Bearer error="insufficient_scope", scope="read"`)
		writeError(c, http.StatusForbidden, "MCP requires OAuth")
		return
	}

	var request mcpRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" {
		handler.writeMCPError(c, request.ID, -32600, "Invalid JSON-RPC request", nil)
		return
	}
	if len(request.ID) == 0 || string(request.ID) == "null" {
		if request.Method == "notifications/initialized" || request.Method == "notifications/cancelled" {
			c.Status(http.StatusAccepted)
			return
		}
	}
	switch request.Method {
	case "initialize":
		handler.writeMCPResult(c, request.ID, gin.H{
			"protocolVersion": "2024-11-05",
			"capabilities":    gin.H{"tools": gin.H{}},
			"serverInfo":      gin.H{"name": "Docmost Go MCP", "version": "go-migration"},
		})
	case "ping":
		handler.writeMCPResult(c, request.ID, gin.H{})
	case "tools/list":
		handler.writeMCPResult(c, request.ID, gin.H{"tools": mcpTools()})
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || strings.TrimSpace(params.Name) == "" {
			handler.writeMCPError(c, request.ID, -32602, "Tool name is required", nil)
			return
		}
		result, err := handler.callMCPTool(c, params.Name, params.Arguments)
		if err != nil {
			handler.writeMCPResult(c, request.ID, mcpToolResult(err.Error(), true))
			return
		}
		handler.writeMCPResult(c, request.ID, mcpToolResultJSON(result))
	default:
		handler.writeMCPError(c, request.ID, -32601, "Method not found", nil)
	}
}

func (handler *Handler) writeMCPResult(c *gin.Context, id json.RawMessage, result any) {
	c.Header("Content-Type", "application/json")
	c.JSON(http.StatusOK, mcpResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (handler *Handler) writeMCPError(c *gin.Context, id json.RawMessage, code int, message string, data any) {
	c.Header("Content-Type", "application/json")
	c.JSON(http.StatusOK, mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: code, Message: message, Data: data}})
}

func mcpTools() []mcpTool {
	object := func(properties map[string]any, required ...string) any {
		schema := gin.H{"type": "object", "properties": properties}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	text := func(description string) any { return gin.H{"type": "string", "description": description} }
	integer := func() any { return gin.H{"type": "integer", "minimum": 1, "maximum": 100} }
	content := gin.H{"anyOf": []any{gin.H{"type": "string"}, gin.H{"type": "object"}}, "description": "Tiptap JSON or plain text"}
	return []mcpTool{
		{Name: "search_pages", Description: "Search pages the current user can access.", InputSchema: object(map[string]any{"query": text("Search text"), "space_id": text("Optional space id"), "limit": integer()}, "query")},
		{Name: "get_page", Description: "Read an accessible page and its editor content.", InputSchema: object(map[string]any{"page_id": text("Page id or slug id")}, "page_id")},
		{Name: "list_pages", Description: "List accessible pages.", InputSchema: object(map[string]any{"space_id": text("Optional space id"), "parent_page_id": text("Optional parent page id"), "limit": integer()})},
		{Name: "list_child_pages", Description: "List direct child pages.", InputSchema: object(map[string]any{"page_id": text("Parent page id")}, "page_id")},
		{Name: "create_page", Description: "Create a page in a writable space.", InputSchema: object(map[string]any{"space_id": text("Space id or slug"), "parent_page_id": text("Optional parent page id"), "title": text("Page title"), "content": content}, "space_id", "title")},
		{Name: "update_page", Description: "Update a writable page.", InputSchema: object(map[string]any{"page_id": text("Page id or slug id"), "title": text("Optional title"), "content": content}, "page_id")},
		{Name: "duplicate_page", Description: "Duplicate a page into a writable space.", InputSchema: object(map[string]any{"page_id": text("Page id"), "target_space_id": text("Optional target space")}, "page_id")},
		{Name: "move_page", Description: "Move a page below another page.", InputSchema: object(map[string]any{"page_id": text("Page id"), "parent_page_id": text("Optional parent page id"), "position": text("Optional position")}, "page_id")},
		{Name: "move_page_to_space", Description: "Move a page and descendants to another space.", InputSchema: object(map[string]any{"page_id": text("Page id"), "space_id": text("Target space")}, "page_id", "space_id")},
		{Name: "get_space", Description: "Read an accessible space.", InputSchema: object(map[string]any{"space_id": text("Space id or slug")}, "space_id")},
		{Name: "list_spaces", Description: "List spaces visible to the current user.", InputSchema: object(map[string]any{"limit": integer()})},
		{Name: "create_space", Description: "Create a space as a workspace administrator.", InputSchema: object(map[string]any{"name": text("Space name"), "description": text("Optional description"), "visibility": text("private or public")}, "name")},
		{Name: "update_space", Description: "Update a space as a workspace administrator.", InputSchema: object(map[string]any{"space_id": text("Space id"), "name": text("Optional name"), "description": text("Optional description"), "visibility": text("Optional visibility")}, "space_id")},
		{Name: "get_comments", Description: "List comments on an accessible page.", InputSchema: object(map[string]any{"page_id": text("Page id")}, "page_id")},
		{Name: "create_comment", Description: "Create a comment on a writable page.", InputSchema: object(map[string]any{"page_id": text("Page id"), "content": content}, "page_id", "content")},
		{Name: "update_comment", Description: "Update a comment created by the current user.", InputSchema: object(map[string]any{"comment_id": text("Comment id"), "content": content}, "comment_id", "content")},
		{Name: "search_attachments", Description: "Search accessible attachment text and names.", InputSchema: object(map[string]any{"query": text("Search text"), "space_id": text("Optional space id"), "limit": integer()}, "query")},
		{Name: "list_workspace_members", Description: "List workspace members.", InputSchema: object(map[string]any{"limit": integer()})},
		{Name: "get_current_user", Description: "Read the authenticated user.", InputSchema: object(map[string]any{})},
	}
}

func (handler *Handler) callMCPTool(c *gin.Context, name string, args map[string]any) (any, error) {
	current := currentPrincipal(c)
	ctx := c.Request.Context()
	limit := mcpLimit(args["limit"])
	read := func() error { return handler.requireMCPRead(c) }
	write := func() error { return handler.requireMCPWrite(c) }
	switch name {
	case "search_pages":
		if err := read(); err != nil {
			return nil, err
		}
		query := mcpString(args, "query")
		if query == "" {
			return nil, errors.New("query is required")
		}
		var spaceID *string
		if value := mcpString(args, "space_id"); value != "" {
			spaceID = &value
		}
		return handler.repository.SearchPages(ctx, current.Workspace.ID, query, spaceID, limit, current.User.ID, isAdmin(current.User))
	case "get_page":
		if err := read(); err != nil {
			return nil, err
		}
		return handler.mcpPage(ctx, current, mcpString(args, "page_id"), false)
	case "list_pages", "list_child_pages":
		if err := read(); err != nil {
			return nil, err
		}
		var spaceID, parentID *string
		if name == "list_child_pages" {
			parent := mcpString(args, "page_id")
			page, err := handler.mcpPage(ctx, current, parent, false)
			if err != nil {
				return nil, err
			}
			parentID = &page.ID
		} else {
			if value := mcpString(args, "space_id"); value != "" {
				spaceID = &value
			}
			if value := mcpString(args, "parent_page_id"); value != "" {
				parentID = &value
			}
		}
		return handler.repository.Pages(ctx, current.Workspace.ID, postgres.PageListFilter{ViewerID: current.User.ID, ViewerAdmin: isAdmin(current.User), SpaceID: spaceID, ParentPageID: parentID, Limit: limit})
	case "create_page":
		if err := write(); err != nil {
			return nil, err
		}
		space, err := handler.repository.SpaceByID(ctx, mcpString(args, "space_id"), current.Workspace.ID, current.User.ID)
		if err != nil || !handler.mcpSpaceWritable(ctx, current, space.ID) {
			return nil, errors.New("write access to space is required")
		}
		title := mcpString(args, "title")
		if title == "" {
			title = "Untitled"
		}
		var parent *string
		if value := mcpString(args, "parent_page_id"); value != "" {
			page, e := handler.mcpPage(ctx, current, value, true)
			if e != nil {
				return nil, e
			}
			parent = &page.ID
		}
		content, err := mcpContent(args["content"])
		if err != nil {
			return nil, err
		}
		return handler.repository.CreatePage(ctx, current.Workspace.ID, current.User.ID, postgres.PageInput{Title: &title, SpaceID: &space.ID, ParentPageID: parent, Content: content})
	case "update_page":
		if err := write(); err != nil {
			return nil, err
		}
		page, err := handler.mcpPage(ctx, current, mcpString(args, "page_id"), true)
		if err != nil {
			return nil, err
		}
		input := postgres.PageInput{}
		if value := mcpString(args, "title"); value != "" {
			input.Title = &value
		}
		if value, exists := args["content"]; exists {
			input.Content, err = mcpContent(value)
			if err != nil {
				return nil, err
			}
		}
		return handler.repository.UpdatePage(ctx, page.ID, current.Workspace.ID, current.User.ID, input)
	case "duplicate_page":
		if err := write(); err != nil {
			return nil, err
		}
		page, err := handler.mcpPage(ctx, current, mcpString(args, "page_id"), false)
		if err != nil {
			return nil, err
		}
		target := mcpString(args, "target_space_id")
		if target == "" {
			target = page.SpaceID
		}
		space, err := handler.repository.SpaceByID(ctx, target, current.Workspace.ID, current.User.ID)
		if err != nil || !handler.mcpSpaceWritable(ctx, current, space.ID) {
			return nil, errors.New("write access to target space is required")
		}
		copy, _, err := handler.repository.DuplicatePage(ctx, page.ID, space.ID, current.Workspace.ID, current.User.ID)
		return copy, err
	case "move_page":
		if err := write(); err != nil {
			return nil, err
		}
		page, err := handler.mcpPage(ctx, current, mcpString(args, "page_id"), true)
		if err != nil {
			return nil, err
		}
		var parent, position *string
		if value := mcpString(args, "parent_page_id"); value != "" {
			p, e := handler.mcpPage(ctx, current, value, false)
			if e != nil {
				return nil, e
			}
			parent = &p.ID
		}
		if value := mcpString(args, "position"); value != "" {
			position = &value
		}
		return gin.H{"pageId": page.ID}, handler.repository.MovePage(ctx, page.ID, current.Workspace.ID, parent, position)
	case "move_page_to_space":
		if err := write(); err != nil {
			return nil, err
		}
		page, err := handler.mcpPage(ctx, current, mcpString(args, "page_id"), true)
		if err != nil {
			return nil, err
		}
		space, err := handler.repository.SpaceByID(ctx, mcpString(args, "space_id"), current.Workspace.ID, current.User.ID)
		if err != nil || !handler.mcpSpaceWritable(ctx, current, space.ID) {
			return nil, errors.New("write access to target space is required")
		}
		return gin.H{"pageId": page.ID, "spaceId": space.ID}, handler.repository.MovePageToSpace(ctx, page.ID, current.Workspace.ID, space.ID)
	case "get_space":
		if err := read(); err != nil {
			return nil, err
		}
		return handler.repository.SpaceByID(ctx, mcpString(args, "space_id"), current.Workspace.ID, current.User.ID)
	case "list_spaces":
		if err := read(); err != nil {
			return nil, err
		}
		return handler.repository.Spaces(ctx, current.Workspace.ID, current.User.ID, limit)
	case "create_space":
		if err := write(); err != nil {
			return nil, err
		}
		if !isAdmin(current.User) {
			return nil, errors.New("workspace administrator access is required")
		}
		name := mcpString(args, "name")
		if name == "" {
			return nil, errors.New("name is required")
		}
		return handler.repository.CreateSpace(ctx, current.Workspace.ID, current.User.ID, postgres.SpaceInput{Name: &name, Description: mcpStringPtr(args, "description"), Visibility: mcpStringPtr(args, "visibility")})
	case "update_space":
		if err := write(); err != nil {
			return nil, err
		}
		if !isAdmin(current.User) {
			return nil, errors.New("workspace administrator access is required")
		}
		return handler.repository.UpdateSpace(ctx, mcpString(args, "space_id"), current.Workspace.ID, current.User.ID, postgres.SpaceInput{Name: mcpStringPtr(args, "name"), Description: mcpStringPtr(args, "description"), Visibility: mcpStringPtr(args, "visibility")})
	case "get_comments":
		if err := read(); err != nil {
			return nil, err
		}
		page, err := handler.mcpPage(ctx, current, mcpString(args, "page_id"), false)
		if err != nil {
			return nil, err
		}
		return handler.repository.Comments(ctx, page.ID, current.Workspace.ID, limit)
	case "create_comment":
		if err := write(); err != nil {
			return nil, err
		}
		page, err := handler.mcpPage(ctx, current, mcpString(args, "page_id"), true)
		if err != nil {
			return nil, err
		}
		content, err := mcpContent(args["content"])
		if err != nil {
			return nil, err
		}
		return handler.repository.CreateComment(ctx, current.Workspace.ID, current.User.ID, postgres.CommentInput{PageID: page.ID, SpaceID: page.SpaceID, Content: content})
	case "update_comment":
		if err := write(); err != nil {
			return nil, err
		}
		id := mcpString(args, "comment_id")
		if id == "" {
			return nil, errors.New("comment_id is required")
		}
		comment, err := handler.repository.CommentByID(ctx, id, current.Workspace.ID)
		if err != nil || comment.CreatorID == nil || *comment.CreatorID != current.User.ID {
			return nil, errors.New("comment not found or not owned by current user")
		}
		content, err := mcpContent(args["content"])
		if err != nil {
			return nil, err
		}
		return handler.repository.UpdateComment(ctx, id, current.Workspace.ID, current.User.ID, content)
	case "search_attachments":
		if err := read(); err != nil {
			return nil, err
		}
		query := mcpString(args, "query")
		if query == "" {
			return nil, errors.New("query is required")
		}
		var spaceID *string
		if value := mcpString(args, "space_id"); value != "" {
			spaceID = &value
		}
		return handler.repository.SearchAttachments(ctx, current.Workspace.ID, query, spaceID, limit, current.User.ID, isAdmin(current.User))
	case "list_workspace_members":
		if err := read(); err != nil {
			return nil, err
		}
		return handler.repository.WorkspaceMembers(ctx, current.Workspace.ID, limit)
	case "get_current_user":
		if err := read(); err != nil {
			return nil, err
		}
		return current.User, nil
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

func (handler *Handler) mcpPage(ctx context.Context, current principal, id string, edit bool) (domain.Page, error) {
	if strings.TrimSpace(id) == "" {
		return domain.Page{}, errors.New("page_id is required")
	}
	page, err := handler.repository.PageByID(ctx, id, id, current.Workspace.ID, false)
	if err != nil {
		return domain.Page{}, errors.New("page not found")
	}
	access, err := handler.repository.PageAccess(ctx, page.ID, current.Workspace.ID, current.User.ID)
	if err != nil || (access.HasRestriction && (!access.CanAccess || (edit && !access.CanEdit))) {
		return domain.Page{}, errors.New("page access denied")
	}
	role, err := handler.repository.SpaceRole(ctx, page.SpaceID, current.Workspace.ID, current.User.ID)
	if err != nil || (edit && !mcpRoleAtLeastString(role, "writer")) {
		return domain.Page{}, errors.New("page access denied")
	}
	return page, nil
}

func (handler *Handler) mcpSpaceWritable(ctx context.Context, current principal, spaceID string) bool {
	role, err := handler.repository.SpaceRole(ctx, spaceID, current.Workspace.ID, current.User.ID)
	return err == nil && mcpRoleAtLeastString(role, "writer")
}

func (handler *Handler) requireMCPRead(c *gin.Context) error {
	current := currentPrincipal(c)
	if current.OAuthGrantID != "" && !mcpHasScope(current.OAuthScopes, "read") {
		return errors.New("OAuth read scope is required")
	}
	return nil
}
func (handler *Handler) requireMCPWrite(c *gin.Context) error {
	if err := handler.requireMCPRead(c); err != nil {
		return err
	}
	current := currentPrincipal(c)
	if current.OAuthGrantID != "" && !mcpHasScope(current.OAuthScopes, "write") {
		return errors.New("OAuth write scope is required")
	}
	return nil
}
func mcpHasScope(scopes []string, wanted string) bool {
	for _, scope := range scopes {
		if scope == wanted {
			return true
		}
	}
	return false
}
func mcpRoleAtLeast(membership *domain.Membership, wanted string) bool {
	return membership != nil && mcpRoleAtLeastString(membership.Role, wanted)
}
func mcpRoleAtLeastString(actual, wanted string) bool {
	rank := map[string]int{"reader": 1, "writer": 2, "admin": 3}
	return rank[actual] >= rank[wanted]
}
func mcpString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}
func mcpStringPtr(args map[string]any, key string) *string {
	value := mcpString(args, key)
	if value == "" {
		return nil
	}
	return &value
}
func mcpLimit(value any) int {
	if number, ok := value.(float64); ok && int(number) > 0 {
		if int(number) > 100 {
			return 100
		}
		return int(number)
	}
	return 50
}
func mcpContent(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil, nil
		}
		var document any
		if json.Unmarshal([]byte(text), &document) == nil {
			return json.RawMessage(text), nil
		}
		return json.Marshal(gin.H{"type": "doc", "content": []any{gin.H{"type": "paragraph", "content": []any{gin.H{"type": "text", "text": text}}}}})
	}
	return json.Marshal(value)
}
func mcpToolResult(value string, isError bool) gin.H {
	return gin.H{"content": []gin.H{{"type": "text", "text": value}}, "isError": isError}
}
func mcpToolResultJSON(value any) gin.H {
	data, err := json.Marshal(value)
	if err != nil {
		return mcpToolResult(err.Error(), true)
	}
	return mcpToolResult(string(data), false)
}
