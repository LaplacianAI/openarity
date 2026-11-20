/**
 * A line diff, for text the brain sends whole before and after. Small and
 * exact rather than clever: the lines both sides share at the start and end
 * are kept, and the longest common subsequence is taken of what lies between.
 * That table is quadratic, so past MAX_CELLS the middle is shown as removed
 * then added: still exact, and a proposal may carry 2 MiB.
 */

const MAX_CELLS = 1_000_000;

export type DiffLine = { kind: "same" | "add" | "del"; text: string };

/** A run of unchanged lines left out, with how many. */
export type Folded = { kind: "skip"; count: number };

function linesOf(text: string | null): string[] {
  if (text === null || text === "") return [];
  return text.replace(/\r\n/g, "\n").split("\n");
}

/** Every line of `after` and `before`, each marked as kept, added or removed. */
export function lineDiff(before: string | null, after: string | null): DiffLine[] {
  const a = linesOf(before);
  const b = linesOf(after);
  let head = 0;
  while (head < a.length && head < b.length && a[head] === b[head]) head++;
  let tail = 0;
  while (
    tail < a.length - head &&
    tail < b.length - head &&
    a[a.length - 1 - tail] === b[b.length - 1 - tail]
  )
    tail++;
  const same = (text: string): DiffLine => ({ kind: "same", text });
  return [
    ...a.slice(0, head).map(same),
    ...middleDiff(a.slice(head, a.length - tail), b.slice(head, b.length - tail)),
    ...a.slice(a.length - tail).map(same),
  ];
}

function middleDiff(a: string[], b: string[]): DiffLine[] {
  if (a.length * b.length > MAX_CELLS) {
    return [
      ...a.map((text): DiffLine => ({ kind: "del", text })),
      ...b.map((text): DiffLine => ({ kind: "add", text })),
    ];
  }
  // lcs[i][j]: the common length of a[i..] and b[j..].
  const lcs: number[][] = Array.from({ length: a.length + 1 }, () =>
    new Array<number>(b.length + 1).fill(0),
  );
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      const row = lcs[i] as number[];
      row[j] =
        a[i] === b[j]
          ? ((lcs[i + 1] as number[])[j + 1] as number) + 1
          : Math.max((lcs[i + 1] as number[])[j] as number, row[j + 1] as number);
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length || j < b.length) {
    if (i < a.length && j < b.length && a[i] === b[j]) {
      out.push({ kind: "same", text: a[i] as string });
      i++;
      j++;
    } else if (
      j >= b.length ||
      (i < a.length &&
        ((lcs[i + 1] as number[])[j] as number) >= ((lcs[i] as number[])[j + 1] as number))
    ) {
      out.push({ kind: "del", text: a[i] as string });
      i++;
    } else {
      out.push({ kind: "add", text: b[j] as string });
      j++;
    }
  }
  return out;
}

/**
 * The diff with long unchanged stretches folded to a count, keeping `context`
 * lines around each change. A stretch is folded only when that hides at least
 * two lines, so "1 unchanged line" never stands where the line itself would.
 */
export function withContext(lines: DiffLine[], context = 2): (DiffLine | Folded)[] {
  const near = lines.map((l) => l.kind !== "same");
  const keep = lines.map((_, i) => {
    for (let d = -context; d <= context; d++) if (near[i + d]) return true;
    return false;
  });
  const out: (DiffLine | Folded)[] = [];
  let i = 0;
  while (i < lines.length) {
    if (keep[i]) {
      out.push(lines[i] as DiffLine);
      i++;
      continue;
    }
    let end = i;
    while (end < lines.length && !keep[end]) end++;
    if (end - i < 2) out.push(...(lines.slice(i, end) as DiffLine[]));
    else out.push({ kind: "skip", count: end - i });
    i = end;
  }
  return out;
}
