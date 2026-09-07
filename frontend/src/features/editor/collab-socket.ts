import {
  HocuspocusProviderWebsocket,
  WebSocketStatus,
} from "@hocuspocus/provider";
import { getCollaborationUrl } from "@/lib/config.ts";

// YGo currently serves one collaboration room per WebSocket connection. Keep
// one socket per page and release it as soon as the last editor leaves that
// page. Delaying the release makes a page switch briefly keep two live
// collaboration sockets, which adds unnecessary work during navigation.
const socketsByPageId = new Map<string, HocuspocusProviderWebsocket>();
const editorCountsByPageId = new Map<string, number>();

export function getCollabSocket(pageId: string): HocuspocusProviderWebsocket {
  let socket = socketsByPageId.get(pageId);
  if (!socket) {
    socket = new HocuspocusProviderWebsocket({
      url: getCollaborationUrl(`page.${pageId}`),
      autoConnect: false,
    });
    socketsByPageId.set(pageId, socket);
  }
  return socket;
}

export function acquireCollabSocket(pageId: string): void {
  editorCountsByPageId.set(pageId, (editorCountsByPageId.get(pageId) ?? 0) + 1);

  const collabSocket = getCollabSocket(pageId);
  collabSocket.shouldConnect = true;
  if (collabSocket.status === WebSocketStatus.Disconnected) {
    collabSocket.connect();
  }
}

export function releaseCollabSocket(pageId: string): void {
  const nextCount = Math.max(0, (editorCountsByPageId.get(pageId) ?? 0) - 1);
  editorCountsByPageId.set(pageId, nextCount);
  if (nextCount > 0) return;

  socketsByPageId.get(pageId)?.disconnect();
  socketsByPageId.delete(pageId);
  editorCountsByPageId.delete(pageId);
}
