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
