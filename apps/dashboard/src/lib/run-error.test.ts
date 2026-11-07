import { describe, expect, it } from "vitest";

import { explainRunError } from "./run-error";

describe("explainRunError", () => {
  it("names the MCP server that could not be reached, and keeps the network's words", () => {
    const raw =
      'the MCP server "statuspage" could not be reached: dial tcp: lookup statuspage.mcp.invalid: no such host';
    expect(explainRunError(raw)).toEqual({
      said: "Couldn't reach the MCP server statuspage.",
      details: raw,
    });
  });

  // Runs stored before the brain stopped naming its setting keep that wording.
  it("says the model could not be reached, whichever way the brain worded it", () => {
    for (const raw of [
      "the model endpoint could not be reached: dial tcp 127.0.0.1:53: refused",
      "the model endpoint OPENARITY_LLM_BASE_URL could not be reached: dial tcp 127.0.0.1:53: refused",
    ]) {
      expect(explainRunError(raw)).toEqual({ said: "Couldn't reach the model.", details: raw });
    }
  });

  it("says the model refused, with its status, old wording or new", () => {
    for (const raw of [
      "the model endpoint answered 401 Unauthorized (invalid_api_key)",
      "OPENARITY_LLM_BASE_URL: the model endpoint answered 401 Unauthorized (invalid_api_key)",
    ]) {
      expect(explainRunError(raw).said).toBe("The model refused the request (401).");
    }
  });

  it("says a run ran out of its token budget", () => {
    expect(explainRunError("the run has spent its token budget").said).toBe(
      "It ran out of its token budget.",
    );
  });

  // Nothing is lost: what the brain said stays readable, never rewritten.
  it("shows an error of no known kind as it was written, with nothing hidden", () => {
    expect(explainRunError("the model provider answered 529: overloaded")).toEqual({
      said: "the model provider answered 529: overloaded",
      details: null,
    });
  });

  it("puts no setting's name in what it says", () => {
    const said = explainRunError(
      "the model endpoint OPENARITY_LLM_BASE_URL could not be reached: x",
    ).said;
    expect(said).not.toMatch(/OPENARITY_/);
  });
});
