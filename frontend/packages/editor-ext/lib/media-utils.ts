import { Editor } from "@tiptap/core";

type RuntimeConfig = {
  API_BASE_URL?: string;
  APP_URL?: string;
};

function getBackendBaseUrl(): string {
  const runtimeConfig =
    typeof window !== "undefined"
      ? (window as Window & { CONFIG?: RuntimeConfig }).CONFIG
      : undefined;
  const buildConfig =
    typeof process !== "undefined" && process.env ? process.env : undefined;

  return (
    runtimeConfig?.API_BASE_URL ||
    buildConfig?.API_BASE_URL ||
    runtimeConfig?.APP_URL ||
    buildConfig?.APP_URL ||
    ""
  );
}

export function normalizeFileUrl(src: string): string {
  if (!src) return "";

  const rawSrc = src.trim();
  let parsed: URL;
  try {
    parsed = new URL(
      rawSrc,
      typeof window !== "undefined" ? window.location.origin : "http://localhost",
    );
  } catch {
    return src;
  }
  const sourceIsAbsolute = /^https?:\/\//i.test(rawSrc);
  const sourcePath = parsed.pathname;
  const suffix = `${parsed.search}${parsed.hash}`;
  if (sourceIsAbsolute && !getBackendBaseUrl()) return src;

  let normalizedPath = sourcePath;
  const storagePath = sourcePath.match(
    /^\/?file\/[^/]+\/(?:files\/)?([^/]+)\/(.+)$/,
  ) || sourcePath.match(/^\/?[^/]+\/files\/([^/]+)\/(.+)$/);
  if (storagePath) {
    normalizedPath = `/api/files/${storagePath[1]}/${storagePath[2]}`;
  } else if (sourcePath.match(/^\/?files\/([^/]+)\/(.+)$/)) {
    normalizedPath = sourcePath.startsWith("/")
      ? "/api" + sourcePath
      : "/api/" + sourcePath;
  }
  if (!normalizedPath.startsWith("/api/files/")) {
    return src;
  }

  const backendBaseUrl = getBackendBaseUrl();
  if (!backendBaseUrl) {
    return normalizedPath + suffix;
  }

  try {
    // An absolute path keeps the API prefix while avoiding the frontend
    // origin when the client is served separately from the Go backend.
    return new URL(normalizedPath + suffix, backendBaseUrl).toString();
  } catch {
    return normalizedPath + suffix;
  }
}

export function getAttachmentFileUrl(attachment: {
  id: string;
  fileName: string;
  url?: string;
}): string {
  const serverUrl = typeof attachment.url === "string" ? attachment.url.trim() : "";
  if (serverUrl) return normalizeFileUrl(serverUrl);

  return normalizeFileUrl(
    `/api/files/${encodeURIComponent(attachment.id)}/${encodeURIComponent(attachment.fileName)}`,
  );
}

/**
 * Set a protected media URL before assigning src. A separately hosted
 * frontend and Go API are different origins, so browser media elements need
 * explicit credential mode in order to send the session cookie. External
 * images/media remain anonymous and are not affected.
 */
export function setAuthenticatedMediaSource(
  element: HTMLImageElement | HTMLMediaElement,
  src: string,
): string {
  const normalized = normalizeFileUrl(src);
  let requiresCredentials = false;

  if (typeof window !== "undefined" && normalized) {
    try {
      const parsed = new URL(normalized, window.location.origin);
      requiresCredentials =
        parsed.origin !== window.location.origin &&
        parsed.pathname.startsWith("/api/files/");
    } catch {
      // Keep the browser's normal source handling for malformed/external URLs.
    }
  }

  element.crossOrigin = requiresCredentials ? "use-credentials" : "";
  element.src = normalized;
  return normalized;
}

export function syncAltBadge(wrapper: HTMLElement, alt: unknown): void {
  const existing = wrapper.querySelector<HTMLElement>(
    ":scope > .media-alt-badge",
  );

  if (typeof alt !== "string" || !alt.trim()) {
    existing?.remove();
    return;
  }

  const badge = existing ?? document.createElement("span");
  badge.dataset.alt = alt;

  if (!existing) {
    badge.className = "media-alt-badge";
    badge.textContent = "ALT";
    badge.setAttribute("aria-hidden", "true");
    wrapper.appendChild(badge);
  }
}

export type UploadFn = (
  file: File,
  editor: Editor,
  pos: number,
  pageId: string,
  // only applicable to file attachments
  allowMedia?: boolean,
) => void;

export interface MediaUploadOptions {
  validateFn?: (file: File, allowMedia?: boolean) => void;
  onUpload: (file: File, pageId: string) => Promise<any>;
}
