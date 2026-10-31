// Config mode of a set: its rules and default as YAML text, and back.
// Loaded on demand (with the editor) so the YAML parser stays out of the
// main bundle.
import { Document, isMap, isNode, isScalar, isSeq, parseDocument, visit, type Node } from 'yaml';
import type { End, Rule } from '@/api/policy';

export interface FlowBody { rules: Rule[]; default?: End }

// `enabled` is written only when false; missing = true.
const forYaml = (rules: Rule[]): unknown[] => rules.map(({ id, name, enabled, when, then }) => ({
  id, ...(name ? { name } : {}), ...(enabled ? {} : { enabled: false }), ...(when ? { when } : {}),
  then: then.type === 'flow' ? { ...then, rules: forYaml(then.rules) } : then,
}));
const fromYaml = (rules: unknown): Rule[] => (Array.isArray(rules) ? rules : []).map((r: any) => ({
  ...r, enabled: r?.enabled !== false,
  ...(r?.then?.type === 'flow' ? { then: { ...r.then, rules: fromYaml(r.then.rules) } } : {}),
}));

/** Position-free message of a YAML error (the line is reported separately). */
const short = (m: string) => m.split('\n')[0].replace(/ at line \d+, column \d+:?$/, '');

/** The YAML text of a flow; references carry the referenced set's name as a comment. */
export function toYaml(body: FlowBody, names: Map<string, string>): string {
  const obj: Record<string, unknown> = { rules: forYaml(body.rules) };
  if (body.default) obj.default = body.default;
  const doc = new Document(obj);
  visit(doc, {
    Pair(_, pair) {
      const k = isScalar(pair.key) ? String(pair.key.value) : '';
      if ((k === 'ref' || k === 'policy') && isScalar(pair.value)) {
        const n = names.get(String(pair.value.value));
        if (n) pair.value.comment = ` ${n}`;
      }
    },
  });
  return doc.toString({ lineWidth: 0 });
}

export type Parsed = { body: FlowBody } | { error: { line: number; msg: string; from: number; to: number } };

/** Parses the config text; the first syntax error with its line on failure. */
export function parseConfig(text: string): Parsed {
  const doc = parseDocument(text);
  const e = doc.errors[0];
  if (e) return { error: { line: e.linePos?.[0]?.line ?? 1, msg: short(e.message), from: e.pos[0], to: Math.max(e.pos[1], e.pos[0] + 1) } };
  const obj = doc.toJS() as Record<string, unknown> | null;
  if (obj !== null && (typeof obj !== 'object' || Array.isArray(obj))) {
    return { error: { line: 1, msg: 'expected a mapping with rules and default', from: 0, to: Math.max(1, text.indexOf('\n')) } };
  }
  return { body: { rules: fromYaml(obj?.rules), default: (obj?.default ?? undefined) as End | undefined } };
}

/** Lint diagnostics of the text (syntax errors and warnings). */
export function lintConfig(text: string): { from: number; to: number; message: string; error: boolean }[] {
  const doc = parseDocument(text);
  return [...doc.errors.map((e) => ({ e, error: true })), ...doc.warnings.map((e) => ({ e, error: false }))].map(({ e, error }) => ({
    from: Math.min(e.pos[0], text.length), to: Math.min(Math.max(e.pos[1], e.pos[0] + 1), text.length), message: short(e.message), error,
  }));
}

/**
 * A validation error path ("rules[1].then.paths[0]") → the closest node's
 * range and line; null when the path is outside the text (e.g. name).
 */
export function locate(text: string, path: string): { from: number; to: number; line: number } | null {
  const doc = parseDocument(text);
  const parts = path.split(/\.|\[(\d+)\]/).filter((x) => x !== undefined && x !== '').map((x) => (/^\d+$/.test(x) ? Number(x) : x));
  for (let n = parts.length; n >= 1; n--) {
    const node = doc.getIn(parts.slice(0, n), true);
    if (!isNode(node) || !(node as Node).range) continue;
    let [from, to] = (node as Node).range!;
    if (isMap(node) || isSeq(node)) {
      const end = text.indexOf('\n', from);
      to = end < 0 ? text.length : end; // a whole map / list: its first line
    }
    return { from, to, line: text.slice(0, from).split('\n').length };
  }
  return null;
}
