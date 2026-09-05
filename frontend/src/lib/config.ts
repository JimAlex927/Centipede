import bytes from "bytes";
import { castToBoolean } from "@/lib/utils.tsx";
import { AvatarIconType } from "@/features/attachments/types/attachment.types.ts";
import { sanitizeUrl } from "@docmost/editor-ext";

declare global {
  interface Window {
    CONFIG?: Record<string, string>;
  }
}

export function getAppName(): string {
  return "Docmost";
}

export function getAppUrl(): string {
  return `${window.location.protocol}//${window.location.host}`;
}

export function getServerAppUrl(): string {
  return getConfigValue("APP_URL");
}

export function getBackendUrl(): string {
  // API_BASE_URL is the preferred explicit setting. APP_URL is kept as the
  // documented backend-origin fallback for a separately served frontend.
  return getConfigValue("API_BASE_URL", `${getServerAppUrl() || getAppUrl()}/api`).replace(/\/$/, "");
}

export function getApiUrl(path: string): string {
  const normalizedPath = path.startsWith("/api/") ? path.substring(4) : path;
  return `${getBackendUrl()}${normalizedPath.startsWith("/") ? normalizedPath : `/${normalizedPath}`}`;
}

export function getBackendOrigin(): string {
  const backendUrl = new URL(getBackendUrl(), getAppUrl());
  return backendUrl.origin;
}

export function getRealtimeUrl(): string {
  return getConfigValue("REALTIME_URL", getAppUrl()).replace(/\/$/, "");
}

export function getRealtimeWebSocketUrl(): string {
  const baseUrl = getConfigValue("REALTIME_URL") || getBackendOrigin();
  const realtimeUrl = new URL("/realtime", baseUrl);
  if (realtimeUrl.protocol === "https:") realtimeUrl.protocol = "wss:";
  if (realtimeUrl.protocol === "http:") realtimeUrl.protocol = "ws:";
  return realtimeUrl.toString();
}

export function getCollaborationUrl(roomName?: string): string {
  // Collaboration is served by Go at /collab.  A separately hosted preview
  // or production frontend must not fall back to its own origin: that origin
  // only serves static files and the Hocuspocus handshake then fails with a
  // generic "real-time editor connection lost" message.  COLLAB_URL remains
  // available for a dedicated websocket gateway.
  const baseUrl = getConfigValue("COLLAB_URL") || getBackendOrigin();

  const collabUrl = new URL(
    roomName ? `/collab/${encodeURIComponent(roomName)}` : "/collab",
    baseUrl,
  );
  collabUrl.protocol = collabUrl.protocol === "https:" ? "wss:" : "ws:";
  return collabUrl.toString();
}

export function getSubdomainHost(): string {
  return getConfigValue("SUBDOMAIN_HOST");
}

export function isCloud(): boolean {
  return castToBoolean(getConfigValue("CLOUD"));
}

export function getAiVectorDriver(): string {
  return getConfigValue("AI_VECTOR_DRIVER");
}

export function getAvatarUrl(
  avatarUrl: string,
  type: AvatarIconType = AvatarIconType.AVATAR,
) {
  if (!avatarUrl) return null;
  if (avatarUrl?.startsWith("http")) return avatarUrl;

  return getBackendUrl() + `/attachments/img/${type}/` + encodeURI(avatarUrl);
}

export function getSpaceUrl(spaceSlug: string) {
  return "/s/" + spaceSlug;
}

export function getFileUrl(src: string) {
  if (!src) return src;

  const rawSrc = src.trim();
  let parsed: URL;
  try {
    parsed = new URL(rawSrc, getAppUrl());
  } catch {
    return sanitizeUrl(src);
  }
  const sourceIsAbsolute = /^https?:\/\//i.test(rawSrc);
  const sourcePath = parsed.pathname;
  const suffix = `${parsed.search}${parsed.hash}`;

  // Absolute URLs can be left over from the Node service or an older Go
  // instance. Rewrite only known attachment paths, and only when the user
  // configured a backend origin; unrelated external URLs must remain intact.
  const configuredBackend = Boolean(
    getConfigValue("API_BASE_URL") || getConfigValue("APP_URL"),
  );
  if (sourceIsAbsolute && !configuredBackend) return sanitizeUrl(src);

  // Older Go-imported pages can persist the storage path instead of the
  // public API path, for example:
  //   file/<workspace-id>/<attachment-id>/<file-name>
  // Keep those documents readable after switching away from the Node
  // monolith.  The workspace segment is intentionally discarded because
  // the protected file endpoint addresses attachments by id.
  const storagePath =
    sourcePath.match(/^\/?file\/[^/]+\/(?:files\/)?([^/]+)\/(.+)$/) ||
    sourcePath.match(/^\/?[^/]+\/files\/([^/]+)\/(.+)$/);
  if (storagePath) {
    return `${getBackendUrl()}/files/${storagePath[1]}/${storagePath[2]}${suffix}`;
  }

  // Also accept the same path without the leading slash. This form can be
  // present in exported/imported HTML and otherwise becomes a frontend-local
  // relative URL.
  const publicPath = sourcePath.match(/^\/?files\/([^/]+)\/(.+)$/);
  if (publicPath) {
    return `${getBackendUrl()}/files/${publicPath[1]}/${publicPath[2]}${suffix}`;
  }

  if (sourcePath.startsWith("/api/files/")) {
    // Remove the '/api' prefix
    return getBackendUrl() + sourcePath.substring(4) + suffix;
  }
  if (sourcePath.startsWith("/files/")) {
    return getBackendUrl() + sourcePath + suffix;
  }
  return sourceIsAbsolute ? sanitizeUrl(src) : sanitizeUrl(rawSrc);
}

export function getFileUploadSizeLimit() {
  const limit = getConfigValue("FILE_UPLOAD_SIZE_LIMIT", "50mb");
  return bytes(limit);
}

export function getFileImportSizeLimit() {
  const limit = getConfigValue("FILE_IMPORT_SIZE_LIMIT", "200mb");
  return bytes(limit);
}

export function getDrawioUrl() {
  return getConfigValue("DRAWIO_URL", "https://embed.diagrams.net");
}

export function getBillingTrialDays() {
  return getConfigValue("BILLING_TRIAL_DAYS");
}

export function getPostHogHost() {
  return getConfigValue("POSTHOG_HOST");
}

export function isPostHogEnabled(): boolean {
  return Boolean(getPostHogHost() && getPostHogKey());
}

export function getPostHogKey() {
  return getConfigValue("POSTHOG_KEY");
}

function getConfigValue(key: string, defaultValue: string = undefined): string {
  // Node's StaticModule supplies window.CONFIG when the legacy monolith
  // serves the client. A standalone build has no runtime HTML injection, so
  // fall back to the values embedded by Vite during the client build.
  const runtimeValue = window?.CONFIG?.[key];
  const buildValue = process?.env?.[key];
  return runtimeValue || buildValue || defaultValue;
}
