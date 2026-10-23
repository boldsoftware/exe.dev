// Human-readable byte sizes, shared by every place that shows one.
//
// This existed twice (the support-modal attachment list and the terminal's
// upload queue) with two slightly different roundings, which is the kind of
// drift that makes a UI look sloppy for no reason. Now it is one function.
//
// Rounding is deliberate: integers below 10 keep a decimal ("1.5 KB") because
// the difference matters at that scale, and larger values are rounded ("42 KB")
// because a tenth of a kilobyte does not.
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return "0 B";
  if (n < 1024) return `${Math.round(n)} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 10 || Number.isInteger(v) ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}
