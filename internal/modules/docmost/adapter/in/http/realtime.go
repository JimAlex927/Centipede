package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"

	"github.com/gorilla/websocket"
)

const (
	realtimeReadLimit = 1 << 20
	realtimeWriteWait = 10 * time.Second
)

type realtimeEnvelope struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

type realtimeClient struct {
	connection *websocket.Conn
	userID     string
	workspace  string
	writeMu    sync.Mutex
}

type realtimeHub struct {
	repository *postgres.Repository
	clients    map[*realtimeClient]struct{}
	mu         sync.RWMutex
}

// RealtimeHandler is the Go replacement for the small Socket.IO event bus
// used by the Docmost client. It intentionally carries JSON event envelopes;
// the client only relies on message and notification events.
type RealtimeHandler struct {
	repository     *postgres.Repository
	tokens         *tokenService
	allowedOrigins map[string]struct{}
	hub            *realtimeHub
	upgrader       websocket.Upgrader
}

func NewRealtimeHandler(repository *postgres.Repository, secret string, allowedOrigins []string) *RealtimeHandler {
	origins := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		origin = strings.TrimRight(strings.TrimSpace(origin), "/")
		if origin != "" {
			origins[origin] = struct{}{}
		}
	}
	handler := &RealtimeHandler{
		repository:     repository,
		tokens:         newTokenService(secret),
		allowedOrigins: origins,
	}
	handler.hub = &realtimeHub{repository: repository, clients: make(map[*realtimeClient]struct{})}
	handler.upgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     handler.checkOrigin,
	}
	return handler
}

func (handler *RealtimeHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	client, ok := handler.authenticate(request)
	if !ok {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	connection, err := handler.upgrader.Upgrade(response, request, nil)
	if err != nil {
		return
	}
	client.connection = connection
	connection.SetReadLimit(realtimeReadLimit)
	handler.hub.add(client)
	defer func() {
		handler.hub.remove(client)
		_ = connection.Close()
	}()

	// http.Server.Shutdown cancels the request context, but a gorilla read is
	// blocked until the socket closes. Close it from a watcher so shutdown does
	// not leave realtime goroutines behind.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-request.Context().Done():
			_ = connection.Close()
		case <-stop:
		}
	}()

	for {
		_, payload, readErr := connection.ReadMessage()
		if readErr != nil {
			return
		}
		var envelope realtimeEnvelope
		if json.Unmarshal(payload, &envelope) != nil || envelope.Event != "message" {
			continue
		}
		var event map[string]any
		if json.Unmarshal(envelope.Data, &event) != nil {
			continue
		}
		spaceID, _ := event["spaceId"].(string)
		if spaceID == "" || !handler.canAccessSpace(request.Context(), client, spaceID) {
			continue
		}
		pageID, valid := realtimePageID(envelope.Data, true)
		if !valid {
			continue
		}
		// Deleted pages no longer have reliable permission records. Send only
		// a refetch signal, never the caller's stale page metadata.
		if event["operation"] == "deleteTreeNode" || event["operation"] == "refetchRootTreeNodeEvent" {
			envelope.Data = marshalRealtimeData(map[string]any{"operation": "refetchRootTreeNodeEvent", "spaceId": spaceID})
		} else if !handler.hub.canAccessPage(client, spaceID, pageID) {
			continue
		}
		handler.hub.broadcast(client, realtimeEnvelope{Event: "message", Data: envelope.Data}, spaceID)
	}
}

// PublishSpaceEvent sends an event to every currently connected member of a
// space. REST handlers use it for events that are produced by the server (for
// example comment mutations), while tree mutations can still use the inbound
// client event path for now.
func (handler *RealtimeHandler) PublishSpaceEvent(workspaceID, spaceID string, data any) {
	handler.hub.broadcastToSpace(workspaceID, spaceID, realtimeEnvelope{Event: "message", Data: marshalRealtimeData(data)}, nil)
}

// PublishNotification targets one user's open tabs. The frontend listens for
// the notification event to invalidate its notification query.
func (handler *RealtimeHandler) PublishNotification(workspaceID, userID string, data any) {
	handler.hub.broadcastToUser(workspaceID, userID, realtimeEnvelope{Event: "notification", Data: marshalRealtimeData(data)})
}

func (handler *RealtimeHandler) authenticate(request *http.Request) (*realtimeClient, bool) {
	cookie, err := request.Cookie("authToken")
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return nil, false
	}
	claims, err := handler.tokens.parse(cookie.Value, "access")
	if err != nil {
		return nil, false
	}
	user, err := handler.repository.UserByID(request.Context(), claims.Subject, claims.WorkspaceID)
	if err != nil || user.DeactivatedAt != nil || user.DeletedAt != nil {
		return nil, false
	}
	return &realtimeClient{userID: user.ID, workspace: claims.WorkspaceID}, true
}

func (handler *RealtimeHandler) checkOrigin(request *http.Request) bool {
	origin := strings.TrimRight(strings.TrimSpace(request.Header.Get("Origin")), "/")
	if origin == "" {
		return true
	}
	// Same-origin access is valid regardless of the configured cross-origin
	// allowlist. This is important for Docker deployments accessed through a
	// LAN IP or a reverse proxy whose host is not known at build time.
	if origin == "http://"+request.Host || origin == "https://"+request.Host {
		return true
	}
	if len(handler.allowedOrigins) > 0 {
		_, ok := handler.allowedOrigins[origin]
		return ok
	}
	return origin == "http://"+request.Host || origin == "https://"+request.Host
}

func (handler *RealtimeHandler) canAccessSpace(ctx context.Context, client *realtimeClient, spaceID string) bool {
	role, err := handler.repository.SpaceRole(ctx, spaceID, client.workspace, client.userID)
	return err == nil && role != ""
}

func (hub *realtimeHub) add(client *realtimeClient) {
	hub.mu.Lock()
	hub.clients[client] = struct{}{}
	hub.mu.Unlock()
}

func (hub *realtimeHub) remove(client *realtimeClient) {
	hub.mu.Lock()
	delete(hub.clients, client)
	hub.mu.Unlock()
}

func (hub *realtimeHub) broadcast(sender *realtimeClient, envelope realtimeEnvelope, spaceID string) {
	hub.broadcastToSpace(sender.workspace, spaceID, envelope, sender)
}

func (hub *realtimeHub) broadcastToSpace(workspaceID, spaceID string, envelope realtimeEnvelope, excluded *realtimeClient) {
	pageID, valid := realtimePageID(envelope.Data, false)
	if !valid {
		return
	}
	hub.mu.RLock()
	clients := make([]*realtimeClient, 0, len(hub.clients))
	for client := range hub.clients {
		if client != excluded && client.workspace == workspaceID {
			clients = append(clients, client)
		}
	}
	hub.mu.RUnlock()

	payload, err := json.Marshal(envelope)
	if err != nil {
		return
	}
	for _, client := range clients {
		if !hub.canAccessSpace(client, spaceID) {
			continue
		}
		if pageID != "" && !hub.canAccessPage(client, spaceID, pageID) {
			continue
		}
		_ = client.write(payload)
	}
}

func (hub *realtimeHub) broadcastToUser(workspaceID, userID string, envelope realtimeEnvelope) {
	hub.mu.RLock()
	clients := make([]*realtimeClient, 0, len(hub.clients))
	for client := range hub.clients {
		if client.workspace == workspaceID && client.userID == userID {
			clients = append(clients, client)
		}
	}
	hub.mu.RUnlock()

	payload, err := json.Marshal(envelope)
	if err != nil {
		return
	}
	for _, client := range clients {
		_ = client.write(payload)
	}
}

func marshalRealtimeData(data any) json.RawMessage {
	payload, err := json.Marshal(data)
	if err != nil {
		return json.RawMessage("null")
	}
	return payload
}

func (hub *realtimeHub) canAccessSpace(client *realtimeClient, spaceID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	role, err := hub.repository.SpaceRole(ctx, spaceID, client.workspace, client.userID)
	return err == nil && role != ""
}

func (hub *realtimeHub) canAccessPage(client *realtimeClient, spaceID, pageID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	actualSpace, deleted, err := hub.repository.CollaborationPage(ctx, pageID, client.workspace)
	if err != nil || deleted || actualSpace != spaceID {
		return false
	}
	access, err := hub.repository.PageAccess(ctx, pageID, client.workspace, client.userID)
	return err == nil && access.CanAccess
}

// Only tree events may originate from browsers. Comment events are emitted
// after successful REST mutations and cannot be forged through this channel.
func realtimePageID(data json.RawMessage, inbound bool) (string, bool) {
	var event struct {
		Operation string `json:"operation"`
		ID        string `json:"id"`
		PageID    string `json:"pageId"`
		Payload   struct {
			ID   string `json:"id"`
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
			Node struct {
				ID string `json:"id"`
			} `json:"node"`
		} `json:"payload"`
	}
	if json.Unmarshal(data, &event) != nil {
		return "", false
	}
	var id string
	switch event.Operation {
	case "refetchRootTreeNodeEvent":
		return "", true
	case "addTreeNode":
		id = event.Payload.Data.ID
	case "moveTreeNode":
		id = event.Payload.ID
	case "deleteTreeNode":
		id = event.Payload.Node.ID
	case "updateOne":
		id = event.ID
	case "commentCreated", "commentUpdated", "commentResolved", "commentDeleted":
		if inbound {
			return "", false
		}
		id = event.PageID
	default:
		return "", false
	}
	return id, id != ""
}

func (client *realtimeClient) write(payload []byte) error {
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	if err := client.connection.SetWriteDeadline(time.Now().Add(realtimeWriteWait)); err != nil {
		return err
	}
	return client.connection.WriteMessage(websocket.TextMessage, payload)
}

var _ http.Handler = (*RealtimeHandler)(nil)
