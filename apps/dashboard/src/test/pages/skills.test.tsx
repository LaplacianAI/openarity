import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import { brain, pageOf, STAMP, show, TEAM, team } from "@/test/brain";

vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  Link: ({ children }: { children: React.ReactNode }) => <a href="/">{children}</a>,
}));

const { Route, folderUpload } = await import("@/routes/ui.skills.index");
const Skills = await pageOf(Route);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const pdf = {
  id: "s1",
  team_id: TEAM,
  name: "pdf",
  description: "Read PDFs",
  source: "github",
  created_at: STAMP,
  updated_at: STAMP,
};

const skills = { "GET /teams/{id}/skills": { body: { items: [pdf] } } };

it("lists the team's skills", async () => {
  brain({ ...team([]), ...skills });
  show(Skills);

  expect(await screen.findByText("pdf")).toBeInTheDocument();
  expect(screen.getByText("Read PDFs")).toBeInTheDocument();
});

// The brain refuses a write without skill:write, so the screen does not offer
// one, and says which permission it would take.
it("offers no write action without skill:write, and says what it would need", async () => {
  brain({ ...team(["agent:write"]), ...skills });
  show(Skills);

  await screen.findByText("pdf");
  expect(await screen.findByText("skill:write")).toBeInTheDocument();
  for (const name of [
    /upload folder/i,
    /upload zip/i,
    /^import$/i,
    /^sync$/i,
    /^delete$/i,
    /upload new version/i,
  ]) {
    expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
  }
});

it("offers every write action with skill:write", async () => {
  brain({ ...team(["skill:write"]), ...skills });
  show(Skills);

  await screen.findByText("pdf");
  for (const name of [
    /upload folder/i,
    /upload zip/i,
    /^import$/i,
    /^sync$/i,
    /^delete$/i,
    /upload new version/i,
  ]) {
    expect(await screen.findByRole("button", { name })).toBeInTheDocument();
  }
  expect(screen.queryByText(/a team admin can grant/i)).not.toBeInTheDocument();
});

it("treats a super admin as holding skill:write in any team", async () => {
  brain({ ...team([], true), ...skills });
  show(Skills);

  expect(await screen.findByRole("button", { name: /upload folder/i })).toBeInTheDocument();
});

// An older brain has no permissions route. Every action is offered and the
// brain's own refusal names the problem; a working install must not look
// read-only because a convenience call failed.
it("offers the actions when permissions cannot be read", async () => {
  brain({ "GET /teams": team([])["GET /teams"] as never, ...skills });
  show(Skills);

  expect(await screen.findByRole("button", { name: /upload folder/i })).toBeInTheDocument();
});

it("imports from the source typed in", async () => {
  const seen = brain({
    ...team(["skill:write"]),
    ...skills,
    "POST /teams/{id}/skills/import": {
      status: 201,
      body: { ...pdf, body: "", files: [], metadata: {} },
    },
  });
  show(Skills);

  await userEvent.type(await screen.findByLabelText(/import from/i), "github:acme/skills/pdf@main");
  await userEvent.click(screen.getByRole("button", { name: /^import$/i }));

  await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
  const post = seen.find((s) => s.method === "POST");
  expect(post?.path).toBe(`/teams/${TEAM}/skills/import`);
  expect(JSON.parse(String(post?.body))).toEqual({ source: "github:acme/skills/pdf@main" });
});

it("shows a skill's files and its SKILL.md as text, not markup", async () => {
  brain({
    ...team([]),
    ...skills,
    "GET /teams/{id}/skills/{skillID}": {
      body: {
        ...pdf,
        source_ref: "github:acme/skills/pdf@main",
        body: '<img src=x onerror="alert(1)">',
        files: [{ path: "scripts/read.py", size: 2048, sha256: "ab", media_type: "text/plain" }],
        metadata: {},
      },
    },
  });
  const { container } = show(Skills);

  await userEvent.click(await screen.findByRole("button", { name: /pdf/ }));
  expect(await screen.findByText("scripts/read.py")).toBeInTheDocument();
  expect(screen.getByText("2 KB")).toBeInTheDocument();
  expect(screen.getByText('<img src=x onerror="alert(1)">')).toBeInTheDocument();
  expect(container.querySelector("img")).toBeNull();
});

it("only an imported skill can be synced", async () => {
  brain({
    ...team(["skill:write"]),
    "GET /teams/{id}/skills": {
      body: { items: [{ ...pdf, id: "s2", name: "mine", source: "upload" }] },
    },
  });
  show(Skills);

  await screen.findByText("mine");
  expect(screen.queryByRole("button", { name: /^sync$/i })).not.toBeInTheDocument();
});

// --- the folder a picker hands back ---

function file(path: string): File {
  const f = new File(["x"], path.split("/").pop() as string);
  Object.defineProperty(f, "webkitRelativePath", { value: path });
  return f;
}

it("sends each file of a folder by its path, and leaves .git out", () => {
  const upload = folderUpload([
    file("pdf/SKILL.md"),
    file("pdf/scripts/read.py"),
    file("pdf/.git/HEAD"),
    file("pdf/.git/objects/ab/cd"),
  ]);

  expect("files" in upload && upload.files.map((f) => f.path)).toEqual([
    "pdf/SKILL.md",
    "pdf/scripts/read.py",
  ]);
});

it("uploads a picked folder as one part per file", async () => {
  const seen = brain({
    ...team(["skill:write"]),
    ...skills,
    "POST /teams/{id}/skills": { status: 201, body: { ...pdf, body: "", files: [], metadata: {} } },
  });
  show(Skills);

  const picker = (await screen.findByLabelText(/skill folder/i)) as HTMLInputElement;
  await userEvent.upload(picker, [file("pdf/SKILL.md"), file("pdf/scripts/read.py")]);

  await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
  const body = seen.find((s) => s.method === "POST")?.body;
  expect(body).toBeInstanceOf(FormData);
  const parts = (body as FormData).getAll("files") as File[];
  expect(parts.map((p) => p.name)).toEqual(["pdf/SKILL.md", "pdf/scripts/read.py"]);
});

// --- a new version of a skill ---

// A replace keeps the skill's id and its grants; the brain names the skill
// from the new SKILL.md and refuses, with a sentence, a version that does not fit.

it("offers no new version without skill:write", async () => {
  brain({ ...team(["agent:write"]), ...skills });
  show(Skills);

  await screen.findByText("pdf");
  expect(screen.queryByRole("button", { name: /upload new version/i })).not.toBeInTheDocument();
});

async function openReplace() {
  await userEvent.click(await screen.findByRole("button", { name: /upload new version/i }));
  expect(await screen.findByText(/agents granted this skill keep it/i)).toBeInTheDocument();
}

const replaced = { ...pdf, description: "Read and split PDFs", body: "", files: [], metadata: {} };

it("sends a new version picked as a folder to that skill, one part per file", async () => {
  const seen = brain({
    ...team(["skill:write"]),
    ...skills,
    "PUT /teams/{id}/skills/{skillID}": { body: replaced },
  });
  show(Skills);

  await openReplace();
  const picker = (await screen.findByLabelText(/new version folder/i)) as HTMLInputElement;
  await userEvent.upload(picker, [file("pdf/SKILL.md"), file("pdf/scripts/read.py")]);

  await waitFor(() => expect(seen.some((s) => s.method === "PUT")).toBe(true));
  const put = seen.find((s) => s.method === "PUT");
  expect(put?.path).toBe(`/teams/${TEAM}/skills/s1`);
  const body = put?.body;
  expect(body).toBeInstanceOf(FormData);
  const parts = (body as FormData).getAll("files") as File[];
  expect(parts.map((p) => p.name)).toEqual(["pdf/SKILL.md", "pdf/scripts/read.py"]);
});

it("sends a new version picked as a zip as the body", async () => {
  const seen = brain({
    ...team(["skill:write"]),
    ...skills,
    "PUT /teams/{id}/skills/{skillID}": { body: replaced },
  });
  show(Skills);

  await openReplace();
  const zip = new File(["PK"], "pdf.zip", { type: "application/zip" });
  await userEvent.upload(
    (await screen.findByLabelText(/new version zip/i)) as HTMLInputElement,
    zip,
  );

  await waitFor(() => expect(seen.some((s) => s.method === "PUT")).toBe(true));
  const put = seen.find((s) => s.method === "PUT");
  expect(put?.path).toBe(`/teams/${TEAM}/skills/s1`);
  expect(put?.body).toBe(zip);
});

it("shows the brain's refusal of a new version in the dialog", async () => {
  brain({
    ...team(["skill:write"]),
    ...skills,
    "PUT /teams/{id}/skills/{skillID}": {
      status: 409,
      text: "a skill named csv already exists in this team",
    },
  });
  show(Skills);

  await openReplace();
  await userEvent.upload(
    (await screen.findByLabelText(/new version zip/i)) as HTMLInputElement,
    new File(["PK"], "csv.zip", { type: "application/zip" }),
  );

  const dialog = await screen.findByRole("dialog");
  await waitFor(() =>
    expect(dialog).toHaveTextContent("a skill named csv already exists in this team"),
  );
});

it("lists the new version once it is in", async () => {
  let version = pdf;
  brain({
    ...team(["skill:write"]),
    "GET /teams/{id}/skills": () => ({ body: { items: [version] } }),
    "PUT /teams/{id}/skills/{skillID}": () => {
      version = { ...pdf, description: "Read and split PDFs" };
      return { body: replaced };
    },
  });
  show(Skills);

  await openReplace();
  await userEvent.upload(
    (await screen.findByLabelText(/new version zip/i)) as HTMLInputElement,
    new File(["PK"], "pdf.zip", { type: "application/zip" }),
  );

  expect(await screen.findByText("Read and split PDFs")).toBeInTheDocument();
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
});
