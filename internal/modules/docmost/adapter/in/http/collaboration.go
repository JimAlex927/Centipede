package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	pathpkg "path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	platformconfig "centipede/internal/platform/config"

	ygows "github.com/reearth/ygo/provider/websocket"
)

// CollaborationHandler wraps ygo so the Go service can expose the same basic
// operational counters as the upstream Docmost collaboration gateway.
type CollaborationHandler struct {
	server         *ygows.Server
	store          *postgres.CollaborationStore
	repository     *postgres.Repository
	tokens         *tokenService
	rooms          map[string]*collaborationRoom
	roomsMu        sync.Mutex
	allowedOrigins map[string]struct{}
	realtime       *RealtimeHandler
	enqueueEmail   func(string)
	enqueueAI      func(string, string)
	connections    atomic.Int64
	documents      atomic.Int64
}

func (handler *CollaborationHandler) SetRealtimeHandler(realtime *RealtimeHandler) {
	handler.realtime = realtime
}

func (handler *CollaborationHandler) SetNotificationEmailEnqueuer(enqueue func(string)) {
	handler.enqueueEmail = enqueue
}

func (handler *CollaborationHandler) SetAIEmbeddingEnqueuer(enqueue func(string, string)) {
	handler.enqueueAI = enqueue
}

func (handler *CollaborationHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	handler.serveMultiplex(response, request)
}

func (handler *CollaborationHandler) Stats() (connections, documents int64) {
	return handler.connections.Load(), handler.documents.Load()
}

// NewCollaborationHandler creates the Hocuspocus-compatible Yjs collaboration
// gateway used by the standalone frontend. The HTTP gateway owns physical
// connections and room membership; YGo owns the CRDT documents, persistence
// workers, and bounded warm-room cache behind it.
func NewCollaborationHandler(repository *postgres.Repository, secret string, allowedOrigins []string, collaborationConfig platformconfig.CollaborationConfig) *CollaborationHandler {
	store := repository.CollaborationStore()
	server := ygows.NewServerWithPersistence(store)
	// Keep recently visited pages warm. YGo still flushes pending updates before
	// marking a room idle, so this only avoids the next LoadDoc/rebuild cycle;
	// it does not weaken durability. MaxResidentRooms bounds the memory cost.
	server.RoomIdleTimeout = collaborationConfig.RoomIdleTimeout
	server.MaxResidentRooms = collaborationConfig.MaxResidentRooms
	server.PersistCoalesceWindow = collaborationConfig.PersistCoalesceWindow
	server.PersistCoalesceMaxWait = collaborationConfig.PersistCoalesceMaxWait
	handler := &CollaborationHandler{
		server:         server,
		store:          store,
		repository:     repository,
		tokens:         newTokenService(secret),
		rooms:          make(map[string]*collaborationRoom),
		allowedOrigins: newOriginSet(allowedOrigins),
	}
	store.SetVersionCallback(func(ctx context.Context, pageID, workspaceID string, actorIDs []string, content []byte) {
		actorID := ""
		if len(actorIDs) > 0 {
			actorID = actorIDs[0]
		}
		mentionDeliveries, _ := repository.CreatePageMentionNotifications(ctx, pageID, workspaceID, actorID, content)
		if handler.realtime != nil {
			for _, delivery := range mentionDeliveries {
				handler.realtime.PublishNotification(workspaceID, delivery.UserID, map[string]any{
					"type": "page.user_mention", "notificationId": delivery.ID, "pageId": pageID,
				})
			}
		}
		for _, delivery := range mentionDeliveries {
			if handler.enqueueEmail != nil {
				handler.enqueueEmail(delivery.ID)
			}
		}
		deliveries, err := repository.CreatePageUpdateNotifications(ctx, pageID, workspaceID, actorID, actorIDs)
		if err != nil {
			return
		}
		for _, delivery := range deliveries {
			if handler.realtime != nil {
				handler.realtime.PublishNotification(workspaceID, delivery.UserID, map[string]any{
					"type": "page.updated", "notificationId": delivery.ID, "pageId": pageID,
				})
			}
			if handler.enqueueEmail != nil {
				handler.enqueueEmail(delivery.ID)
			}
		}
	})
	store.SetUpdateCallback(func(ctx context.Context, pageID, workspaceID string) {
		if handler.enqueueAI != nil {
			handler.enqueueAI(workspaceID, pageID)
		}
	})
	// Docmost coalesces collaboration history roughly every five minutes for
	// established pages. ygo invokes SaveVersion after persistence flushes and
	// the adapter makes duplicate snapshots a no-op.
	server.AutoVersionEvery = 5 * time.Minute
	return handler
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
