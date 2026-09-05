package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	pathpkg "path"
	"strings"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"

	ygows "github.com/reearth/ygo/provider/websocket"
)

// NewCollaborationHandler creates the Hocuspocus-compatible Yjs websocket
// handler used by the standalone frontend. It is intentionally separate from
// the REST handler because ygo owns the websocket room lifecycle.
func NewCollaborationHandler(repository *postgres.Repository, secret string, allowedOrigins []string) *ygows.Server {
	server := ygows.NewServerWithPersistence(repository.CollaborationStore())
	tokens := newTokenService(secret)
	server.HocuspocusFraming = true
	server.AllowedOrigins = allowedOrigins

	// Reject unauthenticated upgrades before ygo sends the initial document
	// state. The browser sends the Go authToken cookie to the websocket host.
	server.Authorize = func(request *http.Request) (ygows.ConnectionConfig, bool) {
		cookie, err := request.Cookie("authToken")
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			return ygows.ConnectionConfig{}, false
		}
		claims, err := tokens.parse(cookie.Value, "access")
		if err != nil {
			return ygows.ConnectionConfig{}, false
		}
		user, err := repository.UserByID(request.Context(), claims.Subject, claims.WorkspaceID)
		if err != nil || user.DeactivatedAt != nil || user.DeletedAt != nil {
			return ygows.ConnectionConfig{}, false
		}
		pageID, err := roomPageID(request.URL.Path)
		if err != nil {
			return ygows.ConnectionConfig{}, false
		}
		readOnly, err := collaborationReadOnly(request.Context(), repository, pageID, claims.WorkspaceID, claims.Subject, pointerValue(user.Role))
		if err != nil {
			return ygows.ConnectionConfig{}, false
		}
		return ygows.ConnectionConfig{ReadOnly: readOnly}, true
	}

	// Hocuspocus sends the collaboration JWT in-band after the websocket
	// upgrade. Validate it too, and calculate read-only access for the page.
	server.OnTokenAuth = func(room, rawToken string) (ygows.ConnectionConfig, error) {
		claims, err := tokens.parse(rawToken, "collab")
		if err != nil {
			return ygows.ConnectionConfig{}, errors.New("invalid collaboration token")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		user, err := repository.UserByID(ctx, claims.Subject, claims.WorkspaceID)
		if err != nil || user.DeactivatedAt != nil || user.DeletedAt != nil {
			return ygows.ConnectionConfig{}, errors.New("user is not active")
		}
		pageID, err := pageIDFromRoomName(room)
		if err != nil {
			return ygows.ConnectionConfig{}, err
		}
		readOnly, err := collaborationReadOnly(ctx, repository, pageID, claims.WorkspaceID, claims.Subject, pointerValue(user.Role))
		if err != nil {
			return ygows.ConnectionConfig{}, errors.New("page access denied")
		}
		return ygows.ConnectionConfig{ReadOnly: readOnly}, nil
	}
	return server
}

func roomPageID(path string) (string, error) {
	return pageIDFromRoomName(pathpkg.Base(path))
}

func pageIDFromRoomName(room string) (string, error) {
	if !strings.HasPrefix(room, "page.") || len(room) <= len("page.") {
		return "", fmt.Errorf("invalid collaboration room")
	}
	return strings.TrimPrefix(room, "page."), nil
}

func collaborationReadOnly(ctx context.Context, repository *postgres.Repository, pageID, workspaceID, userID, userRole string) (bool, error) {
	spaceID, deleted, err := repository.CollaborationPage(ctx, pageID, workspaceID)
	if err != nil {
		return false, err
	}
	if deleted {
		return false, errors.New("page is deleted")
	}
	role, err := repository.SpaceRole(ctx, spaceID, workspaceID, userID)
	admin := userRole == "owner" || userRole == "admin"
	if err != nil && !(admin && errors.Is(err, postgres.ErrNotFound)) {
		return false, err
	}
	access, err := repository.PageAccess(ctx, pageID, workspaceID, userID)
	if err != nil {
		return false, err
	}
	if access.HasRestriction && !access.CanAccess {
		return false, errors.New("page access denied")
	}
	if access.HasRestriction {
		return !access.CanEdit, nil
	}
	return !admin && role != "writer" && role != "admin", nil
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
