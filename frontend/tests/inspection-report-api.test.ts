import { afterEach, expect, it, vi } from "vitest";
import { getInspectionReportHTML } from "../src/api/operations";
vi.mock("../src/api/request", () => ({
  default: {},
  getToken: () => "private-fixture-token",
}));
afterEach(() => vi.unstubAllGlobals());
it("authenticates the online report request through a header and returns HTML without making a download", async () => {
  const fetcher = vi
    .fn()
    .mockResolvedValue({
      ok: true,
      headers: new Headers({ "content-type": "text/html" }),
      text: async () => "<h1>report</h1>",
    });
  vi.stubGlobal("fetch", fetcher);
  expect(await getInspectionReportHTML(3)).toBe("<h1>report</h1>");
  expect(fetcher.mock.calls[0]![0]).toBe(
    "/api/operations/inspection-runs/3/report/html",
  );
  expect(fetcher.mock.calls[0]![1].headers.Authorization).toBe(
    "Bearer private-fixture-token",
  );
});
it("reports a JSON API error even when the server returns HTTP 200", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValue({
        ok: true,
        headers: new Headers({ "content-type": "application/json" }),
        json: async () => ({ code: 16001, message: "该格式报告尚未生成" }),
      }),
  );
  await expect(getInspectionReportHTML(3)).rejects.toThrow(
    "该格式报告尚未生成",
  );
});
