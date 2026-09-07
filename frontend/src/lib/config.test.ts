import { beforeEach, describe, expect, it } from "vitest";
import { isInternalFileUrl } from "@docmost/editor-ext";
import {
  appendCacheBust,
  getAttachmentFileUrl,
  getFileCrossOrigin,
  getFileUrl,
} from "./config";

describe("getFileUrl", () => {
  beforeEach(() => {
    window.CONFIG = { API_BASE_URL: "http://localhost:7788/api" };
  });

  it("resolves the public API file path", () => {
    expect(getFileUrl("/api/files/attachment/image.png")).toBe(
      "http://localhost:7788/api/files/attachment/image.png",
    );
  });

  it("converts a Go storage path to the attachment API", () => {
    expect(
      getFileUrl(
        "file/workspace-1/attachment-1/diagram.drawio.svg?t=123#preview",
      ),
    ).toBe(
      "http://localhost:7788/api/files/attachment-1/diagram.drawio.svg?t=123#preview",
    );
  });

  it("converts the Node-compatible workspace/files storage path", () => {
    expect(
      getFileUrl("workspace-1/files/attachment-1/diagram.drawio.svg"),
    ).toBe("http://localhost:7788/api/files/attachment-1/diagram.drawio.svg");
  });

  it("accepts a relative files path from imported HTML", () => {
    expect(getFileUrl("files/attachment-1/manual.pdf")).toBe(
      "http://localhost:7788/api/files/attachment-1/manual.pdf",
    );
  });

  it("rewrites an absolute legacy attachment URL to the configured backend", () => {
    expect(
      getFileUrl(
        "http://old-docmost.example/file/workspace-1/attachment-1/diagram.drawio.svg?t=123",
      ),
    ).toBe(
      "http://localhost:7788/api/files/attachment-1/diagram.drawio.svg?t=123",
    );
  });

  it("rewrites a localhost attachment URL when no backend is configured", () => {
    window.CONFIG = {};
    expect(
      getFileUrl(
        "http://localhost:8080/api/files/attachment-1/diagram.excalidraw.svg?t=123",
      ),
    ).toBe(
      `${window.location.origin}/api/files/attachment-1/diagram.excalidraw.svg?t=123`,
    );
  });

  it("uses the backend attachment URL when one is returned", () => {
    expect(
      getAttachmentFileUrl({
        id: "attachment-1",
        fileName: "diagram.drawio.svg",
        url: "http://old-docmost.example/api/files/attachment-1/diagram.drawio.svg",
      }),
    ).toBe("http://localhost:7788/api/files/attachment-1/diagram.drawio.svg");
  });

  it("encodes attachment names when the backend does not return a URL", () => {
    expect(
      getAttachmentFileUrl({
        id: "attachment-1",
        fileName: "流程 图.drawio.svg",
      }),
    ).toBe(
      "http://localhost:7788/api/files/attachment-1/%E6%B5%81%E7%A8%8B%20%E5%9B%BE.drawio.svg",
    );
  });

  it("appends cache busting without breaking an existing query", () => {
    expect(appendCacheBust("/api/files/a/diagram.svg", 123)).toBe(
      "/api/files/a/diagram.svg?t=123",
    );
    expect(appendCacheBust("/api/files/a/diagram.svg?download=1", 123)).toBe(
      "/api/files/a/diagram.svg?download=1&t=123",
    );
  });

  it("requests protected cross-origin attachments with cookies", () => {
    expect(
      getFileCrossOrigin("/api/files/attachment-1/diagram.drawio.svg"),
    ).toBe("use-credentials");
    expect(
      getFileCrossOrigin("https://example.com/diagram.svg"),
    ).toBeUndefined();
  });

  it("recognizes legacy storage paths as internal attachments", () => {
    expect(isInternalFileUrl("file/workspace-1/attachment-1/manual.pdf")).toBe(
      true,
    );
    expect(isInternalFileUrl("workspace-1/files/attachment-1/audio.mp3")).toBe(
      true,
    );
    expect(
      isInternalFileUrl(
        "https://old-docmost.example/api/files/attachment-1/manual.pdf",
      ),
    ).toBe(true);
    expect(isInternalFileUrl("https://example.com/manual.pdf")).toBe(false);
  });
});
