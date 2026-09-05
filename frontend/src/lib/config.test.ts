import { beforeEach, describe, expect, it } from "vitest";
import { getFileUrl } from "./config";

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
    expect(getFileUrl("workspace-1/files/attachment-1/diagram.drawio.svg")).toBe(
      "http://localhost:7788/api/files/attachment-1/diagram.drawio.svg",
    );
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
});
