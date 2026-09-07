package http

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/reearth/ygo/awareness"
	"github.com/reearth/ygo/crdt"
	"github.com/reearth/ygo/encoding"
	ygows "github.com/reearth/ygo/provider/websocket"
	ygsync "github.com/reearth/ygo/sync"
)

// Hocuspocus message types. The document name is the first field in every
// frame, allowing one browser WebSocket to carry multiple page rooms.
const (
	collabSync           = uint64(0)
	collabAwareness      = uint64(1)
	collabAuth           = uint64(2)
	collabQueryAwareness = uint64(3)
	collabSyncReply      = uint64(4)
	collabStateless      = uint64(5)
	collabBroadcast      = uint64(6)
	collabClose          = uint64(7)
	collabPing           = uint64(9)
	collabPong           = uint64(10)

	collabAuthToken         = uint64(0)
	collabAuthDenied        = uint64(1)
	collabAuthAuthenticated = uint64(2)

	collabReadLimit      = 64 << 20
	collabWriteWait      = 10 * time.Second
	collabWriteQueueSize = 256
	collabTouchInterval  = time.Minute
)

// collaborationRoom is the active-connection view of a YGo room. YGo owns
// the document, persistence worker, and five-minute warm cache; this map only
// tracks which logical rooms are currently attached to physical sockets.
type collaborationRoom struct {
	name      string
	doc       *crdt.Doc
	awareness *awareness.Awareness
	peers     map[*collaborationConnection]*collaborationPeer
	lastTouch time.Time
	mu        sync.RWMutex
}

type collaborationPeer struct {
	readOnly     bool
	awarenessIDs map[uint64]struct{}
}

type collaborationConnection struct {
	socket    *websocket.Conn
	rooms     map[string]*collaborationPeer
	writeCh   chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

func newOriginSet(origins []string) map[string]struct{} {
	result := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		if origin = strings.TrimRight(strings.TrimSpace(origin), "/"); origin != "" {
			result[origin] = struct{}{}
		}
	}
	return result
}

func (handler *CollaborationHandler) originAllowed(request *http.Request) bool {
	origin := strings.TrimRight(strings.TrimSpace(request.Header.Get("Origin")), "/")
	if origin == "" || origin == "http://"+request.Host || origin == "https://"+request.Host {
		return true
	}
	_, ok := handler.allowedOrigins[origin]
	return ok
}

func (handler *CollaborationHandler) serveMultiplex(response http.ResponseWriter, request *http.Request) {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  16 << 10,
		WriteBufferSize: 16 << 10,
		CheckOrigin: func(request *http.Request) bool {
			return handler.originAllowed(request)
		},
	}
	socket, err := upgrader.Upgrade(response, request, nil)
	if err != nil {
		return
	}
	connection := &collaborationConnection{
		socket:  socket,
		rooms:   make(map[string]*collaborationPeer),
		writeCh: make(chan []byte, collabWriteQueueSize),
		done:    make(chan struct{}),
	}
	handler.connections.Add(1)
	defer func() {
		handler.detachConnection(connection)
		connection.close()
		handler.connections.Add(-1)
	}()

	go connection.writeLoop()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-request.Context().Done():
			connection.close()
		case <-stop:
		case <-connection.done:
		}
	}()

	socket.SetReadLimit(collabReadLimit)
	_ = socket.SetReadDeadline(time.Now().Add(30 * time.Second))
	firstMessage := true
	for {
		_, payload, readErr := socket.ReadMessage()
		if readErr != nil {
			return
		}
		if firstMessage {
			firstMessage = false
			_ = socket.SetReadDeadline(time.Time{})
		}
		handler.handleMultiplexFrame(connection, payload)
	}
}

func (connection *collaborationConnection) writeLoop() {
	for {
		select {
		case <-connection.done:
			return
		case payload := <-connection.writeCh:
			if err := connection.socket.SetWriteDeadline(time.Now().Add(collabWriteWait)); err != nil {
				connection.close()
				return
			}
			if err := connection.socket.WriteMessage(websocket.BinaryMessage, payload); err != nil {
				connection.close()
				return
			}
		}
	}
}

func (connection *collaborationConnection) send(payload []byte) bool {
	select {
	case <-connection.done:
		return false
	default:
	}
	select {
	case connection.writeCh <- payload:
		return true
	case <-connection.done:
		return false
	default:
		// A slow browser must reconnect and resync instead of allowing one
		// physical connection to retain unbounded collaboration state.
		connection.close()
		return false
	}
}

func (connection *collaborationConnection) close() {
	connection.closeOnce.Do(func() {
		close(connection.done)
		_ = connection.socket.Close()
	})
}

func (handler *CollaborationHandler) handleMultiplexFrame(connection *collaborationConnection, payload []byte) {
	roomName, inner, err := decodeCollaborationFrame(payload)
	if err != nil {
		return
	}
	dec := encoding.NewDecoder(inner)
	messageType, err := dec.ReadVarUint()
	if err != nil {
		return
	}

	switch messageType {
	case collabAuth:
		handler.handleRoomAuth(connection, roomName, dec)
	case collabSync, collabSyncReply:
		handler.handleRoomSync(connection, roomName, dec.RemainingBytes())
	case collabAwareness:
		handler.handleRoomAwareness(connection, roomName, dec)
	case collabQueryAwareness:
		handler.handleRoomQueryAwareness(connection, roomName)
	case collabBroadcast:
		handler.handleRoomBroadcast(connection, roomName, dec)
	case collabClose:
		handler.detachRoom(connection, roomName)
	case collabPing:
		connection.send(collaborationFrame(roomName, collaborationMessage(collabPong)))
	}
}

func (handler *CollaborationHandler) handleRoomAuth(connection *collaborationConnection, roomName string, dec *encoding.Decoder) {
	subType, err := dec.ReadVarUint()
	if err != nil || subType != collabAuthToken {
		return
	}
	rawToken, err := dec.ReadVarString()
	if err != nil {
		return
	}
	readOnly, authErr := handler.authenticateRoom(roomName, rawToken)
	if authErr != nil {
		connection.send(collaborationFrame(roomName, collaborationAuthMessage(collabAuthDenied, authErr.Error())))
		return
	}
	room, err := handler.ensureRoom(roomName)
	if err != nil {
		connection.send(collaborationFrame(roomName, collaborationAuthMessage(collabAuthDenied, "room unavailable")))
		return
	}
	room = handler.attachRoom(connection, room, readOnly)
	scope := "read-write"
	if readOnly {
		scope = "readonly"
	}
	connection.send(collaborationFrame(roomName, collaborationAuthMessage(collabAuthAuthenticated, scope)))
	// The client sends its own SyncStep1 after authentication. Waiting for
	// that request avoids YGo's extra proactive SyncStep1/Step2 handshake and
	// keeps the first useful document payload flowing in one direction.
	connection.send(collaborationFrame(roomName, collaborationAwarenessMessage(room.awareness.EncodeUpdate(nil))))
}

func (handler *CollaborationHandler) handleRoomSync(connection *collaborationConnection, roomName string, payload []byte) {
	peer, room := handler.peerRoom(connection, roomName)
	if peer == nil || room == nil {
		return
	}
	handler.touchRoom(room)
	subType, _, err := ygsync.ReadSyncMessage(payload)
	if err != nil {
		return
	}
	if peer.readOnly && subType != ygsync.MsgSyncStep1 {
		return
	}
	reply, err := ygsync.ApplySyncMessage(room.doc, payload, connection)
	if err != nil {
		return
	}
	if reply != nil {
		connection.send(collaborationFrame(roomName, collaborationMessageWithRaw(collabSync, reply)))
		return
	}
	if subType == ygsync.MsgSyncStep1 {
		return
	}
	// SyncReply is a client-side request to apply a sync payload without
	// echoing it back to the sender. Other peers still consume the update as a
	// normal Sync message; the Hocuspocus provider does not expose SyncReply as
	// an incoming message type.
	handler.broadcastRoom(room, connection, collaborationFrame(roomName, collaborationMessageWithRaw(collabSync, payload)))
}

func (handler *CollaborationHandler) handleRoomAwareness(connection *collaborationConnection, roomName string, dec *encoding.Decoder) {
	peer, room := handler.peerRoom(connection, roomName)
	if peer == nil || room == nil || peer.readOnly {
		return
	}
	awarenessUpdate, err := dec.ReadVarBytes()
	if err != nil {
		return
	}
	if err := room.awareness.ApplyUpdate(awarenessUpdate, connection); err != nil {
		return
	}
	trackAwarenessIDs(peer, awarenessUpdate)
	handler.touchRoom(room)
	handler.broadcastRoom(room, connection, collaborationFrame(roomName, collaborationAwarenessMessage(awarenessUpdate)))
}

func (handler *CollaborationHandler) handleRoomQueryAwareness(connection *collaborationConnection, roomName string) {
	_, room := handler.peerRoom(connection, roomName)
	if room == nil {
		return
	}
	connection.send(collaborationFrame(roomName, collaborationAwarenessMessage(room.awareness.EncodeUpdate(nil))))
}

func (handler *CollaborationHandler) handleRoomBroadcast(connection *collaborationConnection, roomName string, dec *encoding.Decoder) {
	_, room := handler.peerRoom(connection, roomName)
	if room == nil {
		return
	}
	payload, err := dec.ReadVarString()
	if err != nil {
		return
	}
	inner := encoding.EncodeBytes(func(enc *encoding.Encoder) {
		enc.WriteVarUint(collabStateless)
		enc.WriteVarString(payload)
	})
	handler.broadcastRoom(room, connection, collaborationFrame(roomName, inner))
}

func (handler *CollaborationHandler) authenticateRoom(roomName, rawToken string) (bool, error) {
	claims, err := handler.tokens.parse(rawToken, "collab")
	if err != nil {
		return false, errors.New("invalid collaboration token")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	user, err := handler.repository.UserByID(ctx, claims.Subject, claims.WorkspaceID)
	if err != nil || user.DeactivatedAt != nil || user.DeletedAt != nil {
		return false, errors.New("user is not active")
	}
	pageID, err := pageIDFromRoomName(roomName)
	if err != nil {
		return false, err
	}
	readOnly, err := collaborationReadOnly(ctx, handler.repository, pageID, claims.WorkspaceID, claims.Subject, pointerValue(user.Role))
	if err != nil {
		return false, errors.New("page access denied")
	}
	if !readOnly {
		handler.store.AddContributor(roomName, claims.Subject)
	}
	return readOnly, nil
}

func (handler *CollaborationHandler) ensureRoom(roomName string) (*collaborationRoom, error) {
	if _, err := pageIDFromRoomName(roomName); err != nil {
		return nil, err
	}
	handler.roomsMu.Lock()
	room := handler.rooms[roomName]
	handler.roomsMu.Unlock()
	if room != nil {
		return room, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Apply's no-op callback is intentional: it loads the YGo room and starts
	// its persistence worker without inventing a document update.
	if err := handler.server.Apply(ctx, roomName, func(*crdt.Doc, func(func(*crdt.Transaction))) {}); err != nil && !errors.Is(err, ygows.ErrNoChanges) {
		return nil, err
	}
	doc := handler.server.GetDoc(roomName)
	aw, ok := handler.server.GetAwareness(roomName)
	if doc == nil || !ok {
		return nil, errors.New("collaboration room is not ready")
	}

	handler.roomsMu.Lock()
	defer handler.roomsMu.Unlock()
	if existing := handler.rooms[roomName]; existing != nil {
		return existing, nil
	}
	room = &collaborationRoom{
		name:      roomName,
		doc:       doc,
		awareness: aw,
		peers:     make(map[*collaborationConnection]*collaborationPeer),
		lastTouch: time.Now(),
	}
	handler.rooms[roomName] = room
	return room, nil
}

func (handler *CollaborationHandler) attachRoom(connection *collaborationConnection, room *collaborationRoom, readOnly bool) *collaborationRoom {
	if _, exists := connection.rooms[room.name]; exists {
		connection.rooms[room.name].readOnly = readOnly
		return room
	}
	handler.roomsMu.Lock()
	if current := handler.rooms[room.name]; current != nil {
		room = current
	} else {
		// The room can be removed between ensureRoom's lookup and this attach.
		// Re-registering the same YGo-backed room keeps that hand-off atomic.
		handler.rooms[room.name] = room
	}
	room.mu.Lock()
	wasEmpty := len(room.peers) == 0
	peer := &collaborationPeer{readOnly: readOnly, awarenessIDs: make(map[uint64]struct{})}
	room.peers[connection] = peer
	room.mu.Unlock()
	handler.roomsMu.Unlock()
	connection.rooms[room.name] = peer
	if wasEmpty {
		handler.documents.Add(1)
	}
	return room
}

func (handler *CollaborationHandler) peerRoom(connection *collaborationConnection, roomName string) (*collaborationPeer, *collaborationRoom) {
	peer := connection.rooms[roomName]
	if peer == nil {
		return nil, nil
	}
	handler.roomsMu.Lock()
	room := handler.rooms[roomName]
	handler.roomsMu.Unlock()
	return peer, room
}

func (handler *CollaborationHandler) broadcastRoom(room *collaborationRoom, excluded *collaborationConnection, payload []byte) {
	room.mu.RLock()
	peers := make([]*collaborationConnection, 0, len(room.peers))
	for peer := range room.peers {
		if peer != excluded {
			peers = append(peers, peer)
		}
	}
	room.mu.RUnlock()
	for _, peer := range peers {
		peer.send(payload)
	}
}

func (handler *CollaborationHandler) touchRoom(room *collaborationRoom) {
	room.mu.Lock()
	if time.Since(room.lastTouch) < collabTouchInterval {
		room.mu.Unlock()
		return
	}
	room.lastTouch = time.Now()
	room.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = handler.server.Apply(ctx, room.name, func(*crdt.Doc, func(func(*crdt.Transaction))) {})
}

func (handler *CollaborationHandler) detachRoom(connection *collaborationConnection, roomName string) {
	peer := connection.rooms[roomName]
	if peer == nil {
		return
	}
	delete(connection.rooms, roomName)
	handler.roomsMu.Lock()
	room := handler.rooms[roomName]
	if room == nil {
		handler.roomsMu.Unlock()
		return
	}

	room.mu.Lock()
	delete(room.peers, connection)
	remaining := make([]*collaborationConnection, 0, len(room.peers))
	for other := range room.peers {
		remaining = append(remaining, other)
	}
	removal := encodeAwarenessRemoval(room.awareness, peer.awarenessIDs)
	if len(removal) > 0 {
		_ = room.awareness.ApplyUpdate(removal, connection)
	}
	empty := len(room.peers) == 0
	if empty && handler.rooms[roomName] == room {
		delete(handler.rooms, roomName)
	}
	room.mu.Unlock()
	handler.roomsMu.Unlock()
	if len(removal) > 0 {
		frame := collaborationFrame(roomName, collaborationAwarenessMessage(removal))
		for _, other := range remaining {
			other.send(frame)
		}
	}
	if empty {
		handler.documents.Add(-1)
	}
}

func (handler *CollaborationHandler) detachConnection(connection *collaborationConnection) {
	for roomName := range connection.rooms {
		handler.detachRoom(connection, roomName)
	}
}

func decodeCollaborationFrame(payload []byte) (string, []byte, error) {
	dec := encoding.NewDecoder(payload)
	roomName, err := dec.ReadVarString()
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(roomName) == "" {
		return "", nil, errors.New("empty collaboration room")
	}
	return roomName, dec.RemainingBytes(), nil
}

func collaborationFrame(roomName string, inner []byte) []byte {
	return encoding.EncodeBytes(func(enc *encoding.Encoder) {
		enc.WriteVarString(roomName)
		enc.WriteRaw(inner)
	})
}

func collaborationMessage(messageType uint64) []byte {
	return encoding.EncodeBytes(func(enc *encoding.Encoder) { enc.WriteVarUint(messageType) })
}

func collaborationMessageWithRaw(messageType uint64, payload []byte) []byte {
	return encoding.EncodeBytes(func(enc *encoding.Encoder) {
		enc.WriteVarUint(messageType)
		enc.WriteRaw(payload)
	})
}

func collaborationAuthMessage(messageType uint64, value string) []byte {
	return encoding.EncodeBytes(func(enc *encoding.Encoder) {
		enc.WriteVarUint(collabAuth)
		enc.WriteVarUint(messageType)
		enc.WriteVarString(strings.ToValidUTF8(value, "�"))
	})
}

func collaborationAwarenessMessage(update []byte) []byte {
	return encoding.EncodeBytes(func(enc *encoding.Encoder) {
		enc.WriteVarUint(collabAwareness)
		enc.WriteVarBytes(update)
	})
}

func trackAwarenessIDs(peer *collaborationPeer, update []byte) {
	dec := encoding.NewDecoder(update)
	count, err := dec.ReadVarUint()
	if err != nil || count > 100000 {
		return
	}
	for i := uint64(0); i < count; i++ {
		clientID, idErr := dec.ReadVarUint()
		if idErr != nil {
			return
		}
		if _, clockErr := dec.ReadVarUint(); clockErr != nil {
			return
		}
		if _, valueErr := dec.ReadVarBytes(); valueErr != nil {
			return
		}
		peer.awarenessIDs[clientID] = struct{}{}
	}
}

func encodeAwarenessRemoval(state *awareness.Awareness, clientIDs map[uint64]struct{}) []byte {
	ids := make([]uint64, 0, len(clientIDs))
	for clientID := range clientIDs {
		if _, ok := state.Meta(clientID); ok {
			ids = append(ids, clientID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return encoding.EncodeBytes(func(enc *encoding.Encoder) {
		enc.WriteVarUint(uint64(len(ids)))
		for _, clientID := range ids {
			meta, _ := state.Meta(clientID)
			enc.WriteVarUint(clientID)
			enc.WriteVarUint(meta.Clock + 1)
			enc.WriteVarString("null")
		}
	})
}
