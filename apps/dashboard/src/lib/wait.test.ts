import { expect, it } from "vitest";

import { waitWords } from "./wait";

it("says a wait the way a person would", () => {
  expect(waitWords(1)).toBe("1 minute");
  expect(waitWords(45)).toBe("45 minutes");
  expect(waitWords(60)).toBe("1 hour");
  expect(waitWords(90)).toBe("1 hour 30 minutes");
  expect(waitWords(150)).toBe("2 hours 30 minutes");
  expect(waitWords(1440)).toBe("1 day");
  expect(waitWords(1439)).toBe("23 hours 59 minutes");
});
