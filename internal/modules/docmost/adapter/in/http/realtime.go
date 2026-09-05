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
		handler.hub.broadcast(client, realtimeEnvelope{Event: "message", Data: envelope.Data}, spaceID)
	}
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
	hub.mu.RLock()
	clients := make([]*realtimeClient, 0, len(hub.clients))
	for client := range hub.clients {
		if client != sender && client.workspace == sender.workspace {
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
		_ = client.write(payload)
	}
}

func (hub *realtimeHub) canAccessSpace(client *realtimeClient, spaceID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	role, err := hub.repository.SpaceRole(ctx, spaceID, client.workspace, client.userID)
	return err == nil && role != ""
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
