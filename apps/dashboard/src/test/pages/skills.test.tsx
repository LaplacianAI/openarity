import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import { folderUpload, heldBy, sourceWords } from "@/lib/skills";
import { brain, pageOf, type Seen, STAMP, show, TEAM, team } from "@/test/brain";

const { Route } = await import("@/routes/ui.skills.index");
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
  agent_count: 0,
};

const skills = { "GET /teams/{id}/skills": { body: { items: [pdf] } } };

const whole = { ...pdf, body: "# pdf", files: [], metadata: {} };

/** The row a skill's name sits in. */
function rowOf(name: string): HTMLElement {
  return screen.getByRole("button", { name }).closest("tr") as HTMLElement;
}

/** Opens a skill's sheet from its row. */
async function opened(name: string): Promise<HTMLElement> {
  await userEvent.click(await screen.findByRole("button", { name }));
  return screen.findByRole("dialog", { name });
}

function posts(seen: Seen[]) {
  return seen.filter((s) => s.method === "POST");
}

describe("a skill in words", () => {
  it.each([
    ["github", "GitHub"],
    ["url", "Zip from a link"],
    ["upload", "Uploaded"],
  ])("a %s source reads %s", (source, said) => {
    expect(sourceWords(source)).toBe(said);
  });

  it.each([
    [0, "No agent"],
    [1, "1 agent"],
    [3, "3 agents"],
  ])("held by %i reads %s", (n, said) => {
    expect(heldBy({ ...pdf, agent_count: n })).toBe(said);
  });
});

describe("the skills list", () => {
  it("lists each skill with where it came from and who holds it", async () => {
    brain({
      ...team([]),
      "GET /teams/{id}/skills": {
        body: {
          items: [pdf, { ...pdf, id: "s2", name: "csv", source: "upload", agent_count: 2 }],
        },
      },
    });
    show(Skills);

    await screen.findByRole("button", { name: "pdf" });
    expect(within(rowOf("pdf")).getByText("Read PDFs")).toBeInTheDocument();
    expect(within(rowOf("pdf")).getByText("GitHub")).toBeInTheDocument();
    expect(within(rowOf("pdf")).getByText("No agent")).toBeInTheDocument();
    expect(within(rowOf("csv")).getByText("Uploaded")).toBeInTheDocument();
    expect(within(rowOf("csv")).getByText("2 agents")).toBeInTheDocument();
  });

  // Only the whole list can say how many no agent holds: a page is not a total.
  it("says how many skills no agent holds, once the whole list is in", async () => {
    brain({
      ...team([]),
      "GET /teams/{id}/skills": {
        body: { items: [pdf, { ...pdf, id: "s2", name: "csv", agent_count: 1 }] },
      },
    });
    show(Skills);
    expect(await screen.findByText("1 skill no agent holds.")).toBeInTheDocument();
  });

  it("says nothing of a count while more are to load", async () => {
    brain({ ...team([]), "GET /teams/{id}/skills": { body: { items: [pdf], next_cursor: "c" } } });
    show(Skills);
    await screen.findByRole("button", { name: "pdf" });
    expect(screen.queryByText(/no agent holds/)).toBeNull();
  });

  it("says why the skills could not be listed", async () => {
    brain({ ...team([]), "GET /teams/{id}/skills": { status: 500, text: "the store is down" } });
    show(Skills);
    expect(await screen.findByRole("alert")).toHaveTextContent("the store is down");
  });
});

describe("what may be done", () => {
  // The brain refuses a write without skill:write, so the screen does not
  // offer one, and says which permission it would take.
  it("offers no write action without skill:write, and says what it would need", async () => {
    brain({
      ...team(["agent:write"]),
      ...skills,
      "GET /teams/{id}/skills/{skillID}": { body: whole },
    });
    show(Skills);

    await screen.findByRole("button", { name: "pdf" });
    expect(await screen.findByText("skill:write")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /add skill/i })).toBeNull();
    const sheet = await opened("pdf");
    for (const name of [/^sync$/i, /^delete$/i, /upload new version/i]) {
      expect(within(sheet).queryByRole("button", { name })).toBeNull();
    }
  });

  it("offers adding a skill, and syncing, a new version and deleting one", async () => {
    brain({
      ...team(["skill:write"]),
      ...skills,
      "GET /teams/{id}/skills/{skillID}": { body: whole },
    });
    show(Skills);

    expect(await screen.findByRole("button", { name: /add skill/i })).toBeInTheDocument();
    const sheet = await opened("pdf");
    for (const name of [/^sync$/i, /^delete$/i, /upload new version/i]) {
      expect(within(sheet).getByRole("button", { name })).toBeInTheDocument();
    }
    expect(screen.queryByText(/a team admin can grant/i)).toBeNull();
  });

  it("treats a super admin as holding skill:write in any team", async () => {
    brain({ ...team([], true), ...skills });
    show(Skills);
    expect(await screen.findByRole("button", { name: /add skill/i })).toBeInTheDocument();
  });

  // An older brain has no permissions route. Every action is offered and the
  // brain's own refusal names the problem; a working install must not look
  // read-only because a convenience call failed.
  it("offers the actions when permissions cannot be read", async () => {
    brain({ "GET /teams": team([])["GET /teams"] as never, ...skills });
    show(Skills);
    expect(await screen.findByRole("button", { name: /add skill/i })).toBeInTheDocument();
  });

  it("only an imported skill can be synced", async () => {
    const mine = { ...pdf, id: "s2", name: "mine", source: "upload" };
    brain({
      ...team(["skill:write"]),
      "GET /teams/{id}/skills": { body: { items: [mine] } },
      "GET /teams/{id}/skills/{skillID}": { body: { ...whole, ...mine } },
    });
    show(Skills);

    const sheet = await opened("mine");
    expect(within(sheet).queryByRole("button", { name: /^sync$/i })).toBeNull();
  });
});

describe("a skill's sheet", () => {
  it("shows its files and its SKILL.md as text, not markup", async () => {
    brain({
      ...team([]),
      ...skills,
      "GET /teams/{id}/skills/{skillID}": {
        body: {
          ...whole,
          source_ref: "github:acme/skills/pdf@main",
          body: '<img src=x onerror="alert(1)">',
          files: [{ path: "scripts/read.py", size: 2048, sha256: "ab", media_type: "text/plain" }],
        },
      },
    });
    show(Skills);

    const sheet = await opened("pdf");
    expect(await within(sheet).findByText("scripts/read.py")).toBeInTheDocument();
    expect(within(sheet).getByText("2 KB")).toBeInTheDocument();
    expect(within(sheet).getByText("github:acme/skills/pdf@main")).toBeInTheDocument();
    expect(within(sheet).getByText('<img src=x onerror="alert(1)">')).toBeInTheDocument();
    expect(sheet.querySelector("img")).toBeNull();
  });

  // The brain refuses to delete a skill an agent holds; the sheet says so
  // where Delete would be.
  it("offers no delete for a skill an agent holds, and says why", async () => {
    const held = { ...pdf, agent_count: 2 };
    brain({
      ...team(["skill:write"]),
      "GET /teams/{id}/skills": { body: { items: [held] } },
      "GET /teams/{id}/skills/{skillID}": { body: { ...whole, ...held } },
    });
    show(Skills);

    const sheet = await opened("pdf");
    expect(within(sheet).queryByRole("button", { name: /^delete$/i })).toBeNull();
    expect(within(sheet).getByText(/take it from them before deleting it/)).toBeInTheDocument();
  });

  it("deletes a skill nobody holds once it is confirmed", async () => {
    const seen = brain({
      ...team(["skill:write"]),
      ...skills,
      "GET /teams/{id}/skills/{skillID}": { body: whole },
      "DELETE /teams/{id}/skills/{skillID}": { status: 204 },
    });
    show(Skills);

    const sheet = await opened("pdf");
    await userEvent.click(within(sheet).getByRole("button", { name: /^delete$/i }));
    expect(seen.some((s) => s.method === "DELETE")).toBe(false);
    const confirm = await screen.findByRole("dialog", { name: "Delete pdf?" });
    await userEvent.click(within(confirm).getByRole("button", { name: /^delete$/i }));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(seen.find((s) => s.method === "DELETE")?.path).toBe(`/teams/${TEAM}/skills/s1`);
  });

  it("syncs an imported skill from its source", async () => {
    const seen = brain({
      ...team(["skill:write"]),
      ...skills,
      "GET /teams/{id}/skills/{skillID}": { body: whole },
      "POST /teams/{id}/skills/{skillID}/sync": { body: whole },
    });
    show(Skills);

    const sheet = await opened("pdf");
    await userEvent.click(within(sheet).getByRole("button", { name: /^sync$/i }));
    await waitFor(() =>
      expect(posts(seen).map((s) => s.path)).toEqual([`/teams/${TEAM}/skills/s1/sync`]),
    );
  });
});

// --- adding a skill ---

function file(path: string): File {
  const f = new File(["x"], path.split("/").pop() as string);
  Object.defineProperty(f, "webkitRelativePath", { value: path });
  return f;
}

describe("adding a skill", () => {
  async function adding() {
    await userEvent.click(await screen.findByRole("button", { name: /add skill/i }));
    return screen.findByRole("dialog", { name: "Add a skill" });
  }

  it("imports from the source typed in, then opens what came in", async () => {
    const seen = brain({
      ...team(["skill:write"]),
      ...skills,
      "POST /teams/{id}/skills/import": { status: 201, body: whole },
      "GET /teams/{id}/skills/{skillID}": { body: whole },
    });
    show(Skills);

    const sheet = await adding();
    await userEvent.type(
      within(sheet).getByLabelText(/import from/i),
      "github:acme/skills/pdf@main",
    );
    await userEvent.click(within(sheet).getByRole("button", { name: /^import$/i }));

    await waitFor(() => expect(posts(seen)).toHaveLength(1));
    const post = posts(seen)[0];
    expect(post?.path).toBe(`/teams/${TEAM}/skills/import`);
    expect(JSON.parse(String(post?.body))).toEqual({ source: "github:acme/skills/pdf@main" });
    expect(await screen.findByRole("dialog", { name: "pdf" })).toBeInTheDocument();
  });

  it("shows the brain's refusal in the sheet, and stays", async () => {
    brain({
      ...team(["skill:write"]),
      ...skills,
      "POST /teams/{id}/skills/import": {
        status: 400,
        text: "the source could not be reached: GitHub answered 503",
      },
    });
    show(Skills);

    const sheet = await adding();
    await userEvent.type(within(sheet).getByLabelText(/import from/i), "github:acme/x@main");
    await userEvent.click(within(sheet).getByRole("button", { name: /^import$/i }));

    expect(await within(sheet).findByRole("alert")).toHaveTextContent("GitHub answered 503");
    expect(screen.getByRole("dialog", { name: "Add a skill" })).toBeInTheDocument();
  });

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
      "POST /teams/{id}/skills": { status: 201, body: whole },
      "GET /teams/{id}/skills/{skillID}": { body: whole },
    });
    show(Skills);

    const sheet = await adding();
    const picker = within(sheet).getByLabelText(/skill folder/i) as HTMLInputElement;
    await userEvent.upload(picker, [file("pdf/SKILL.md"), file("pdf/scripts/read.py")]);

    await waitFor(() => expect(posts(seen)).toHaveLength(1));
    const body = posts(seen)[0]?.body;
    expect(body).toBeInstanceOf(FormData);
    const parts = (body as FormData).getAll("files") as File[];
    expect(parts.map((p) => p.name)).toEqual(["pdf/SKILL.md", "pdf/scripts/read.py"]);
  });
});

// --- a new version of a skill ---

// A replace keeps the skill's id and its grants; the brain names the skill
// from the new SKILL.md and refuses, with a sentence, a version that does not fit.
describe("a new version", () => {
  const replaced = { ...whole, description: "Read and split PDFs" };

  async function openReplace() {
    const sheet = await opened("pdf");
    await userEvent.click(within(sheet).getByRole("button", { name: /upload new version/i }));
    expect(await screen.findByText(/agents granted this skill keep it/i)).toBeInTheDocument();
  }

  it("is sent as a folder to that skill, one part per file", async () => {
    const seen = brain({
      ...team(["skill:write"]),
      ...skills,
      "GET /teams/{id}/skills/{skillID}": { body: whole },
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

  it("is sent as a zip as the body", async () => {
    const seen = brain({
      ...team(["skill:write"]),
      ...skills,
      "GET /teams/{id}/skills/{skillID}": { body: whole },
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
    expect(seen.find((s) => s.method === "PUT")?.body).toBe(zip);
  });

  it("shows the brain's refusal in the dialog", async () => {
    brain({
      ...team(["skill:write"]),
      ...skills,
      "GET /teams/{id}/skills/{skillID}": { body: whole },
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

    const dialog = await screen.findByRole("dialog", { name: /upload a new version/i });
    await waitFor(() =>
      expect(dialog).toHaveTextContent("a skill named csv already exists in this team"),
    );
  });

  it("is listed once it is in", async () => {
    let version = pdf;
    brain({
      ...team(["skill:write"]),
      "GET /teams/{id}/skills": () => ({ body: { items: [version] } }),
      "GET /teams/{id}/skills/{skillID}": { body: whole },
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

    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: /upload a new version/i })).toBeNull(),
    );
    expect((await screen.findAllByText("Read and split PDFs")).length).toBeGreaterThan(0);
  });
});
