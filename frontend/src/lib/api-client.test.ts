import { describe, expect, it } from "vitest";
import { unwrapApiData } from "@/lib/api-client.ts";

describe("unwrapApiData", () => {
  it("unwraps the Go API envelope", () => {
    const attachment = { id: "attachment-1", fileName: "diagram.svg" };

    expect(
      unwrapApiData({ data: attachment, success: true, status: 200 }),
    ).toEqual(attachment);
  });

  it("keeps the Node bare upload response", () => {
    const attachment = { id: "attachment-1", fileName: "diagram.svg" };

    expect(unwrapApiData(attachment)).toBe(attachment);
  });

  it("does not unwrap ordinary payloads that happen to contain data", () => {
    const payload = { data: "document-content", kind: "page" };

    expect(unwrapApiData(payload)).toBe(payload);
  });
});
