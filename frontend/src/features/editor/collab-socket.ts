import {
  HocuspocusProviderWebsocket,
  WebSocketStatus,
} from "@hocuspocus/provider";
import { getCollaborationUrl } from "@/lib/config.ts";

// The Go collaboration gateway multiplexes Hocuspocus rooms on one physical
// WebSocket. Keep this socket at tab scope so changing pages only attaches a
// new logical room instead of opening another TCP/WebSocket connection.
let socket: HocuspocusProviderWebsocket | null = null;
let editorCount = 0;
let releaseTimer: ReturnType<typeof setTimeout> | null = null;

export function getCollabSocket(): HocuspocusProviderWebsocket {
	if (!socket) {
		socket = new HocuspocusProviderWebsocket({
			url: getCollaborationUrl(),
			autoConnect: false,
		});
	}
	return socket;
}

export function acquireCollabSocket(): void {
	editorCount += 1;
	if (releaseTimer) {
		clearTimeout(releaseTimer);
		releaseTimer = null;
	}

	const collabSocket = getCollabSocket();
	collabSocket.shouldConnect = true;
	if (collabSocket.status === WebSocketStatus.Disconnected) {
		collabSocket.connect();
	}
}

export function releaseCollabSocket(): void {
	editorCount = Math.max(0, editorCount - 1);
	if (editorCount > 0 || releaseTimer) return;

	// React can unmount the old page before mounting the new one. Defer the
	// disconnect by one task so a normal page switch reuses this socket, while
	// leaving the editor entirely still releases the connection.
	releaseTimer = setTimeout(() => {
		releaseTimer = null;
		if (editorCount === 0) socket?.disconnect();
	}, 0);
}
