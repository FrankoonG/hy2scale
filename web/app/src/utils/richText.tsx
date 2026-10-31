import type { ReactNode } from 'react';

/** Renders "<b>…</b>" spans of a translated string as bold; everything else as text (no HTML). */
export function boldMarkup(s: string): ReactNode {
  const parts = s.split(/<b>(.*?)<\/b>/g);
  return parts.map((p, i) => (i % 2 ? <b key={i}>{p}</b> : p));
}
