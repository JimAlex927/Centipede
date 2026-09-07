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

// Keep the physical connection warm for the same window as the server's
// resident-room cache. A route transition can unmount the old editor before
// mounting the next one; closing the socket in that gap creates a reconnect
// storm and makes Chrome show a long list of `collab` sockets.
const SOCKET_IDLE_TIMEOUT = 5 * 60 * 1000;

export function getCollabSocket(): HocuspocusProviderWebsocket {
	if (!socket) {
		socket = new HocuspocusProviderWebsocket({
			url: getCollaborationUrl(),
			autoConnect: false,
			// With no attached room, awareness is temporarily silent. Give the
			// shared socket enough time to remain idle without the provider's
			// liveness checker forcing a reconnect during a page transition.
			messageReconnectTimeout: SOCKET_IDLE_TIMEOUT,
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

	// React can unmount the old page before mounting the new one. Keep the
	// physical connection alive for the cache window so a normal page switch
	// reuses it, while leaving the editor entirely still releases it eventually.
	releaseTimer = setTimeout(() => {
		releaseTimer = null;
		if (editorCount === 0) socket?.disconnect();
	}, SOCKET_IDLE_TIMEOUT);
}
