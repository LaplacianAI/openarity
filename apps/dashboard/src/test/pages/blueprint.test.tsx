import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { BlueprintPlan } from "@/api/types";
import { brain, pageOf, type Seen, showAt, team } from "@/test/brain";

const { Route } = await import("@/routes/ui.blueprint");
await pageOf(Route);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const PLAN_PATH = "/teams/{id}/blueprints/plan";
const APPLY_PATH = "/teams/{id}/blueprints/apply";

const toChange: BlueprintPlan = {
  changes: [
    { kind: "skill", name: "support-faq", action: "create" },
    {
      kind: "agent",
      name: "triage",
      action: "update",
      fields: ["instructions", "skills"],
      id: "a1",
    },
    { kind: "mcp_server", name: "helpdesk", action: "unchanged", id: "m1" },
  ],
  refusals: [],
  digest: "sha256:shown",
};

const refused: BlueprintPlan = {
  changes: [{ kind: "agent", name: "triage", action: "create" }],
  refusals: [
    {
      kind: "agent",
      name: "triage",
      message: 'skills: "faq" is not a skill in this team or this blueprint',
    },
  ],
  digest: "sha256:refused",
};

const yaml = "agents:\n  - name: triage\n    max_steps: 5\n";

function open() {
  return showAt("/ui/blueprint", [["/ui/blueprint", Route]]);
}

async function write(text: string) {
  const box = await screen.findByLabelText("Blueprint");
  await userEvent.click(box);
  await userEvent.paste(text);
}

function calls(seen: Seen[], path: string): Seen[] {
  const re = new RegExp(`^${path.replace("{id}", "[^/]+")}$`);
  return seen.filter((s) => s.method === "POST" && re.test(s.path));
}

function file(path: string, body = "x"): File {
  const f = new File([body], path.split("/").pop() as string);
  Object.defineProperty(f, "webkitRelativePath", { value: path });
  return f;
}

describe("the blueprint screen", () => {
  it("plans the text and shows every change, writing nothing until Apply", async () => {
    const seen = brain({ ...team([]), [`POST ${PLAN_PATH}`]: { body: toChange } });
    open();

    await write(yaml);
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));

    expect(await screen.findByText("1 to create, 1 to change, 1 unchanged.")).toBeInTheDocument();
    expect(screen.getByText("instructions, skills")).toBeInTheDocument();
    expect(screen.getByText("helpdesk")).toBeInTheDocument();
    expect(JSON.parse(String(calls(seen, PLAN_PATH)[0]?.body))).toEqual({
      agents: [{ name: "triage", max_steps: 5 }],
    });
    expect(calls(seen, APPLY_PATH)).toHaveLength(0);
  });

  it("applies the plan it showed, with its digest, and says what it did", async () => {
    const seen = brain({
      ...team([]),
      [`POST ${PLAN_PATH}`]: { body: toChange },
      [`POST ${APPLY_PATH}`]: {
        body: {
          changes: [
            { kind: "skill", name: "support-faq", action: "create", id: "s1" },
            { kind: "agent", name: "triage", action: "update", id: "a1" },
          ],
        },
      },
    });
    open();

    await write(yaml);
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));
    await userEvent.click(await screen.findByRole("button", { name: "Apply" }));

    expect(await screen.findByRole("heading", { name: "Applied" })).toBeInTheDocument();
    expect(JSON.parse(String(calls(seen, APPLY_PATH)[0]?.body))).toEqual({
      blueprint: { agents: [{ name: "triage", max_steps: 5 }] },
      plan_digest: "sha256:shown",
    });
  });

  it("offers no Apply while the plan has refusals, and shows each one", async () => {
    brain({ ...team([]), [`POST ${PLAN_PATH}`]: { body: refused } });
    open();

    await write(yaml);
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));

    expect(await screen.findByText(/"faq" is not a skill/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Apply" })).toBeNull();
  });

  it("offers no Apply when there is nothing to change", async () => {
    brain({
      ...team([]),
      [`POST ${PLAN_PATH}`]: {
        body: {
          changes: [{ kind: "agent", name: "triage", action: "unchanged", id: "a1" }],
          refusals: [],
          digest: "d",
        },
      },
    });
    open();

    await write(yaml);
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));

    expect(await screen.findByText("Nothing to change.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Apply" })).toBeNull();
  });

  // An edit after planning would otherwise apply a plan of text no longer on
  // the screen.
  it("takes the plan away as soon as the text changes", async () => {
    brain({ ...team([]), [`POST ${PLAN_PATH}`]: { body: toChange } });
    open();

    await write(yaml);
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));
    await screen.findByRole("button", { name: "Apply" });

    await write("#");
    expect(screen.queryByRole("button", { name: "Apply" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "Plan" })).toBeNull();
  });

  it("shows the new plan when the team changed, and applies nothing", async () => {
    const newer = {
      ...toChange,
      digest: "sha256:new",
      changes: [{ kind: "agent", name: "triage", action: "update", fields: ["model"] }],
    };
    brain({
      ...team([]),
      [`POST ${PLAN_PATH}`]: { body: toChange },
      [`POST ${APPLY_PATH}`]: { status: 409, text: JSON.stringify(newer) },
    });
    open();

    await write(yaml);
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));
    await userEvent.click(await screen.findByRole("button", { name: "Apply" }));

    expect(await screen.findByText(/The team changed since that plan/)).toBeInTheDocument();
    expect(screen.getByText("model")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Applied" })).toBeNull();
  });

  it("says the brain's sentence when an apply fails for another reason", async () => {
    brain({
      ...team([]),
      [`POST ${PLAN_PATH}`]: { body: toChange },
      [`POST ${APPLY_PATH}`]: {
        status: 409,
        text: "the team changed while this was applied: plan again",
      },
    });
    open();

    await write(yaml);
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));
    await userEvent.click(await screen.findByRole("button", { name: "Apply" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("plan again");
  });

  it("refuses text that is not a blueprint without asking the brain", async () => {
    const seen = brain({ ...team([]), [`POST ${PLAN_PATH}`]: { body: toChange } });
    open();

    await write("agent:\n  - name: a\n");
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      '"agent" is not part of a blueprint',
    );
    expect(calls(seen, PLAN_PATH)).toHaveLength(0);
  });

  it("asks for the folder a skill path names, then sends it as the skill", async () => {
    const seen = brain({ ...team([]), [`POST ${PLAN_PATH}`]: { body: toChange } });
    open();

    await write("skills:\n  - path: ./skills/support-faq\n");
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Add the folder for ./skills/support-faq",
    );
    expect(calls(seen, PLAN_PATH)).toHaveLength(0);

    await userEvent.upload(screen.getByLabelText("Skill folder"), [
      file("support-faq/SKILL.md", "hi"),
      file("support-faq/.git/HEAD"),
    ]);
    expect(screen.getByText("support-faq")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));

    await waitFor(() => expect(calls(seen, PLAN_PATH)).toHaveLength(1));
    expect(JSON.parse(String(calls(seen, PLAN_PATH)[0]?.body))).toEqual({
      skills: [{ files: [{ path: "support-faq/SKILL.md", data: "aGk=" }] }],
    });
  });

  it("lets an added folder be removed", async () => {
    brain({ ...team([]) });
    open();

    await userEvent.upload(await screen.findByLabelText("Skill folder"), [file("pdf/SKILL.md")]);
    await userEvent.click(screen.getByRole("button", { name: "Remove pdf" }));
    expect(screen.queryByText("pdf")).toBeNull();
  });
});
