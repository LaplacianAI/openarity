import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";

import { Choice } from "./choices";

const OPTIONS = [
  { value: "none", label: "Off" },
  { value: "brave", label: "Brave Search" },
] as const;

it("shows the chosen option's label", () => {
  render(<Choice id="c" value="brave" options={OPTIONS} onChange={() => {}} />);
  expect(screen.getByRole("combobox")).toHaveTextContent("Brave Search");
});

it("shows its placeholder with nothing chosen", () => {
  render(<Choice id="c" value="" options={OPTIONS} onChange={() => {}} placeholder="Choose one" />);
  expect(screen.getByRole("combobox")).toHaveTextContent("Choose one");
});

it("shows a value the list does not offer as it is, rather than a blank field", () => {
  render(
    <Choice
      id="c"
      value={"exa" as "none"}
      options={OPTIONS}
      onChange={() => {}}
      placeholder="Choose one"
    />,
  );
  expect(screen.getByRole("combobox")).toHaveTextContent("exa");
});
