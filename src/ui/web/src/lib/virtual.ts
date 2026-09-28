// Window math for fixed-row-height virtual lists.

export interface VirtualWindow {
  /** first row index to render (inclusive) */
  start: number;
  /** last row index to render (exclusive) */
  end: number;
  /** height of all rows */
  totalHeight: number;
}

export function computeWindow(scrollTop: number, viewportHeight: number, rowHeight: number, total: number, overscan = 10): VirtualWindow {
  const totalHeight = Math.max(0, total * rowHeight);
  if (total <= 0 || rowHeight <= 0) return { start: 0, end: 0, totalHeight };
  const first = Math.floor(Math.max(0, scrollTop) / rowHeight);
  const visible = Math.ceil(Math.max(0, viewportHeight) / rowHeight) + 1;
  const start = Math.max(0, Math.min(total - 1, first - overscan));
  const end = Math.min(total, first + visible + overscan);
  return { start, end: Math.max(start, end), totalHeight };
}

/** pagesFor lists the page indexes covering rows [start, end). */
export function pagesFor(start: number, end: number, pageSize: number): number[] {
  if (end <= start) return [];
  const out: number[] = [];
  for (let p = Math.floor(start / pageSize); p <= Math.floor((end - 1) / pageSize); p++) out.push(p);
  return out;
}

/** isAtBottom tells whether a scroll container is scrolled to (within slack px of) the bottom. */
export function isAtBottom(scrollTop: number, clientHeight: number, scrollHeight: number, slack = 4): boolean {
  return scrollTop + clientHeight >= scrollHeight - slack;
}

/** scrollTopToReveal returns the scrollTop needed to show row index, or null if already visible. */
export function scrollTopToReveal(index: number, rowHeight: number, scrollTop: number, viewportHeight: number): number | null {
  const top = index * rowHeight;
  const bottom = top + rowHeight;
  if (top < scrollTop) return top;
  if (bottom > scrollTop + viewportHeight) return bottom - viewportHeight;
  return null;
}
