import {
  HocuspocusProviderWebsocket,
  WebSocketStatus,
} from "@hocuspocus/provider";
import { getCollaborationUrl } from "@/lib/config.ts";

const RELEASE_GRACE_MS = 5000;

const sockets = new Map<string, HocuspocusProviderWebsocket>();
const editorCounts = new Map<string, number>();
const releaseTimers = new Map<string, ReturnType<typeof setTimeout>>();

export function getCollabSocket(pageId: string): HocuspocusProviderWebsocket {
  let socket = sockets.get(pageId);
  if (!socket) {
    socket = new HocuspocusProviderWebsocket({
      url: getCollaborationUrl(`page.${pageId}`),
      autoConnect: false,
    });
    sockets.set(pageId, socket);
  }
  return socket;
}

export function acquireCollabSocket(pageId: string): void {
  editorCounts.set(pageId, (editorCounts.get(pageId) ?? 0) + 1);
  const releaseTimer = releaseTimers.get(pageId);
  if (releaseTimer) {
    clearTimeout(releaseTimer);
    releaseTimers.delete(pageId);
  }
  const collabSocket = getCollabSocket(pageId);
  collabSocket.shouldConnect = true;
  if (collabSocket.status === WebSocketStatus.Disconnected) {
    collabSocket.connect();
  }
}

export function releaseCollabSocket(pageId: string): void {
  const nextCount = Math.max(0, (editorCounts.get(pageId) ?? 0) - 1);
  editorCounts.set(pageId, nextCount);
  if (nextCount > 0) return;
  const previousTimer = releaseTimers.get(pageId);
  if (previousTimer) clearTimeout(previousTimer);
  const releaseTimer = setTimeout(() => {
    releaseTimers.delete(pageId);
    if ((editorCounts.get(pageId) ?? 0) === 0) {
      sockets.get(pageId)?.disconnect();
      sockets.delete(pageId);
      editorCounts.delete(pageId);
    }
  }, RELEASE_GRACE_MS);
  releaseTimers.set(pageId, releaseTimer);
}
