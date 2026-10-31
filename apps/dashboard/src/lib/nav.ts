/**
 * Whether a sidebar item is the page being shown. The brain serves the
 * dashboard at /ui/, and the router keeps the slash it was given, so a
 * trailing slash is dropped before comparing: without that, Overview is never
 * active on the page a browser lands on. A section holds its own path and the
 * paths below it, not every path that merely starts with its name.
 */
export function isActive(pathname: string, item: { to: string; exact: boolean }): boolean {
  const path = pathname.length > 1 ? pathname.replace(/\/+$/, "") : pathname;
  if (path === item.to) return true;
  return !item.exact && path.startsWith(`${item.to}/`);
}
