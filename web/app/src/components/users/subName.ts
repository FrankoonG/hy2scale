/** Subscription entry names: the template the server fills (app/subscription.go FormatLinkName). */

/** The template when none is set. */
export const SUB_NAME_DEFAULT = '{user} {node} {policy} {proto} {lan}';
export const SUB_NAME_PLACEHOLDERS = ['user', 'node', 'policy', 'proto', 'lan', 'host'] as const;
export type SubNameValues = Record<(typeof SUB_NAME_PLACEHOLDERS)[number], string>;
/** Longest template the server takes (characters). */
export const SUB_NAME_MAX = 128;

// Go RE2's \s: ASCII blanks only
const EMPTY_PAIRS = /\([\t\n\f\r ]*\)|\[[\t\n\f\r ]*\]|（[\t\n\f\r ]*）|【[\t\n\f\r ]*】|<[\t\n\f\r ]*>/g;
const BLANKS = /[\t\n\f\r ]+/g;

/**
 * Fills a name template as the server does: known {words} replaced, others
 * kept as written, brackets left empty removed, blanks collapsed.
 */
export function formatLinkName(tmpl: string, vals: Partial<SubNameValues>): string {
  if (tmpl.trim() === '') tmpl = SUB_NAME_DEFAULT;
  let out = '';
  for (let i = 0; i < tmpl.length;) {
    if (tmpl[i] === '{') {
      const j = tmpl.indexOf('}', i);
      if (j > i) {
        const k = tmpl.slice(i + 1, j);
        if (Object.prototype.hasOwnProperty.call(vals, k)) {
          out += (vals as Record<string, string>)[k] ?? '';
          i = j + 1;
          continue;
        }
      }
    }
    out += tmpl[i];
    i++;
  }
  for (;;) {
    const n = out.replace(EMPTY_PAIRS, '');
    if (n === out) break;
    out = n;
  }
  return out.replace(BLANKS, ' ').trim();
}
