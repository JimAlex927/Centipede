package http

import (
	"testing"

	"github.com/reearth/ygo/awareness"
	"github.com/reearth/ygo/encoding"
)

func TestCollaborationFrameKeepsRoomAndPayloadTogether(t *testing.T) {
	wantPayload := collaborationAuthMessage(collabAuthAuthenticated, "read-write")
	frame := collaborationFrame("page.one", wantPayload)

	room, payload, err := decodeCollaborationFrame(frame)
	if err != nil {
		t.Fatalf("decodeCollaborationFrame() error = %v", err)
	}
	if room != "page.one" {
		t.Fatalf("room = %q, want %q", room, "page.one")
	}
	if string(payload) != string(wantPayload) {
		t.Fatalf("payload changed during framing")
	}
}

func TestEncodeAwarenessRemovalContainsOnlyKnownClients(t *testing.T) {
	state := awareness.New(1)
	update := encoding.EncodeBytes(func(enc *encoding.Encoder) {
		enc.WriteVarUint(1)
		enc.WriteVarUint(42)
		enc.WriteVarUint(1)
		enc.WriteVarString(`{"user":"editor"}`)
	})
	if err := state.ApplyUpdate(update, nil); err != nil {
		t.Fatalf("ApplyUpdate() error = %v", err)
	}

	removal := encodeAwarenessRemoval(state, map[uint64]struct{}{42: {}, 99: {}})
	dec := encoding.NewDecoder(removal)
	count, err := dec.ReadVarUint()
	if err != nil {
		t.Fatalf("read removal count: %v", err)
	}
	if count != 1 {
		t.Fatalf("removal count = %d, want 1", count)
	}
	id, err := dec.ReadVarUint()
	if err != nil || id != 42 {
		t.Fatalf("removed client id = %d, error = %v", id, err)
	}
	clock, err := dec.ReadVarUint()
	if err != nil || clock != 2 {
		t.Fatalf("removal clock = %d, error = %v", clock, err)
	}
	value, err := dec.ReadVarString()
	if err != nil || value != "null" {
		t.Fatalf("removal value = %q, error = %v", value, err)
	}
}
