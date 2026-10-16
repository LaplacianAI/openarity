import { afterEach, expect, it, vi } from "vitest";

import { request } from "./client";
import { skillBody } from "./index";

afterEach(() => {
  vi.unstubAllGlobals();
});

function capture() {
  const mock = vi.fn<typeof fetch>(async () => new Response("{}", { status: 201 }));
  vi.stubGlobal("fetch", mock);
  return mock;
}

// The browser writes a FormData's Content-Type, boundary and all. Setting one
// here would send a multipart body the brain cannot split.
it("sends a FormData without a Content-Type of its own", async () => {
  const mock = capture();
  const form = new FormData();
  form.append("files", new File(["x"], "SKILL.md"), "SKILL.md");

  await request("/teams/t/skills", { method: "POST", upload: form });

  const init = mock.mock.calls[0]?.[1];
  expect(init?.body).toBe(form);
  expect(new Headers(init?.headers).has("Content-Type")).toBe(false);
});

it("sends a zip as itself, with its type", async () => {
  const mock = capture();
  const zip = new File(["PK"], "pdf.zip", { type: "application/zip" });

  await request("/teams/t/skills", { method: "POST", upload: zip });

  const init = mock.mock.calls[0]?.[1];
  expect(init?.body).toBe(zip);
  expect(new Headers(init?.headers).get("Content-Type")).toBe("application/zip");
});

it("names a file with no type as bytes rather than nothing", async () => {
  const mock = capture();
  await request("/x", { method: "POST", upload: new Blob(["PK"]) });
  expect(new Headers(mock.mock.calls[0]?.[1]?.headers).get("Content-Type")).toBe(
    "application/octet-stream",
  );
});

it("builds one files part per file, named by its path", () => {
  const body = skillBody({
    files: [
      { path: "pdf/SKILL.md", file: new File(["a"], "SKILL.md") },
      { path: "pdf/scripts/read.py", file: new File(["b"], "read.py") },
    ],
  });

  expect(body).toBeInstanceOf(FormData);
  const parts = (body as FormData).getAll("files") as File[];
  expect(parts.map((p) => p.name)).toEqual(["pdf/SKILL.md", "pdf/scripts/read.py"]);
});

it("passes a zip through untouched", () => {
  const zip = new File(["PK"], "pdf.zip");
  expect(skillBody({ zip })).toBe(zip);
});
