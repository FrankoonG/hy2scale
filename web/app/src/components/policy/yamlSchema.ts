// Completion data for the YAML view of a set: which keys and values fit
// where the cursor is. Context comes from the indentation of the lines
// above (block-style YAML, as the editor writes it).
import { FIELDS, SOURCE_KINDS, type SetKind } from './shared';

export interface YamlData {
  kind: SetKind;
  conditions: { id: string; name: string }[];
  policies: { id: string; name: string }[];
  users: { id: string; name: string }[];
  ifaces: string[];
  exitPaths: string[];
}

export interface Suggestion { label: string; detail?: string; type: 'property' | 'enum' | 'variable' | 'keyword' }

const LINE = /^(\s*)(- )?(?:([A-Za-z_]+):(?:\s+(.*))?)?/;

interface Parsed { indent: number; dash: boolean; keyCol: number; key?: string; value?: string }

function parse(line: string): Parsed {
  const m = LINE.exec(line)!;
  const indent = m[1].length;
  const dash = !!m[2];
  return { indent, dash, keyCol: indent + (dash ? 2 : 0), key: m[3], value: m[4] };
}

/** The ancestor keys of a position at column col on line n (innermost last). */
function ancestors(lines: string[], n: number, col: number): string[] {
  const out: string[] = [];
  for (let i = n - 1; i >= 0 && col > 0; i--) {
    const l = lines[i];
    if (!l.trim() || l.trim().startsWith('#')) continue;
    const p = parse(l);
    if (p.key && p.keyCol < col && !p.value) {
      out.unshift(p.key);
      col = p.keyCol;
    } else if (p.dash && p.indent < col) {
      col = p.indent + 1; // inside this list item: its parent key sits further left
    } else if (p.keyCol < col && p.key) {
      col = p.keyCol; // a sibling-less scalar further left: we left its mapping
    }
  }
  return out;
}

/** The value of key among the siblings of line n (same key column). */
function sibling(lines: string[], n: number, keyCol: number, key: string): string | undefined {
  const at = (i: number) => {
    const p = parse(lines[i]);
    return p.key && p.keyCol === keyCol ? p : undefined;
  };
  for (let i = n; i >= 0; i--) {
    const p = parse(lines[i]);
    if (lines[i].trim() && p.keyCol < keyCol && !(p.dash && p.keyCol === keyCol)) break;
    const s = at(i);
    if (s?.key === key) return s.value?.trim();
    if (p.dash && p.keyCol === keyCol) break;
  }
  for (let i = n + 1; i < lines.length; i++) {
    const p = parse(lines[i]);
    if (lines[i].trim() && (p.keyCol < keyCol || (p.dash && p.indent < keyCol))) break;
    const s = at(i);
    if (s?.key === key) return s.value?.trim();
  }
  return undefined;
}

const COND_KEYS = ['type', 'field', 'values', 'ref', 'items', 'item'];
const ACTION_KEYS: Record<SetKind, string[]> = {
  policy: ['type', 'reject', 'paths', 'mode', 'iface', 'src', 'policy', 'rules', 'default'],
  condition: ['type', 'result', 'rules', 'default'],
};
const ENDS: Record<SetKind, string[]> = {
  policy: ['exit', 'default', 'iface', 'call', 'deny'],
  condition: ['result'],
};

const keys = (ks: string[]): Suggestion[] => ks.map((k) => ({ label: k, type: 'property' }));
const enums = (vs: string[]): Suggestion[] => vs.map((v) => ({ label: v, type: 'enum' }));
const named = (xs: { id: string; name: string }[]): Suggestion[] => xs.map((x) => ({ label: x.id, detail: x.name, type: 'variable' }));

/** What the scope is under the given ancestors. */
function scope(anc: string[]): 'top' | 'rule' | 'cond' | 'then' | 'default' | 'list' {
  const last = anc[anc.length - 1];
  if (!last) return 'top';
  if (last === 'rules') return 'rule';
  if (last === 'when' || last === 'items' || last === 'item') return 'cond';
  if (last === 'then') return 'then';
  if (last === 'default') return 'default';
  return 'list';
}

/**
 * Suggestions at (line n, column col) of text. `from` is the column where
 * the word being completed starts.
 */
export function suggest(text: string, n: number, col: number, d: YamlData): { from: number; options: Suggestion[] } | null {
  const lines = text.split('\n');
  const line = lines[n] ?? '';
  const before = line.slice(0, col);
  const p = parse(before);
  // value of `key: <word>`
  const kv = /^(\s*)(- )?([A-Za-z_]+):\s+(\S*)$/.exec(before);
  if (kv) {
    const key = kv[3];
    const from = col - kv[4].length;
    const anc = ancestors(lines, n, p.keyCol);
    const sc = scope(anc);
    let options: Suggestion[] = [];
    switch (key) {
      case 'type':
        options = enums(sc === 'cond' ? ['match', 'ref', 'all', 'any', 'not']
          : sc === 'then' ? [...ENDS[d.kind], 'flow']
          : sc === 'default' || sc === 'top' ? ENDS[d.kind] : []);
        break;
      case 'field': options = FIELDS.map((f) => ({ label: f.field, type: 'enum' as const })); break;
      case 'ref': options = named(d.conditions); break;
      case 'policy': options = named(d.policies); break;
      case 'mode': options = enums(['quality', 'aggregate']); break;
      case 'iface': options = enums(d.ifaces); break;
      case 'enabled': case 'reject': case 'result': options = enums(['true', 'false']); break;
    }
    return options.length ? { from, options } : null;
  }
  // list item `- <word>` under values / paths
  const item = /^(\s*)- (\S*)$/.exec(before);
  if (item) {
    const from = col - item[2].length;
    const anc = ancestors(lines, n, item[1].length + 1);
    const parent = anc[anc.length - 1];
    if (parent === 'paths') return { from, options: enums(d.exitPaths) };
    if (parent === 'values') {
      // the field sits beside `values:` — one column left of the dash
      let keyCol = item[1].length;
      for (let i = n - 1; i >= 0; i--) {
        const q = parse(lines[i]);
        if (q.key === 'values' && q.keyCol <= keyCol) { keyCol = q.keyCol; break; }
      }
      const field = sibling(lines, n, keyCol, 'field');
      if (field === 'src_kind') return { from, options: enums(SOURCE_KINDS) };
      if (field === 'proto') return { from, options: enums(['tcp', 'udp']) };
      if (field === 'user') return { from, options: named(d.users) };
      if (field === 'in_iface') return { from, options: enums(d.ifaces) };
      return null;
    }
    // a new mapping item: keys of a rule / condition
  }
  // a key: `<indent>(- )?<word>`
  const k = /^(\s*)(- )?([A-Za-z_]*)$/.exec(before);
  if (k) {
    const keyCol = k[1].length + (k[2] ? 2 : 0);
    const from = col - k[3].length;
    const anc = ancestors(lines, n, keyCol);
    switch (scope(anc)) {
      case 'top': return { from, options: keys(['name', 'rules', 'default']) };
      case 'rule': return { from, options: keys(['name', 'enabled', 'when', 'then', 'id']) };
      case 'cond': return { from, options: keys(COND_KEYS) };
      case 'then': case 'default': return { from, options: keys(ACTION_KEYS[d.kind]) };
    }
  }
  return null;
}
