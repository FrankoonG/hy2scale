import type { TFunction } from 'i18next';
import type { Cond, CondField, Terminal, PolicySet, ConditionSet } from '@/api/policy';

/** Client-side id of a new rule (sets get theirs from the node). */
export const newId = () => 'r' + Math.random().toString(36).slice(2, 10);

export type ValueInput = 'text' | 'choice' | 'user';

export interface FieldInfo { field: CondField; input: ValueInput; choices?: string[]; placeholder?: string }

export const SOURCE_KINDS = ['local', 'forward', 'socks5', 'http', 'ss', 'hy2', 'ikev2', 'l2tp', 'openvpn', 'wireguard', 'access', 'relay'];

/** Fields of a connection, in the order the editor offers them. */
export const FIELDS: FieldInfo[] = [
  { field: 'src_kind', input: 'choice', choices: SOURCE_KINDS },
  { field: 'user', input: 'user' },
  { field: 'src_node', input: 'text', placeholder: 'node' },
  { field: 'src_ip', input: 'text', placeholder: '192.168.1.0/24' },
  { field: 'src_port', input: 'text', placeholder: '1024-65535' },
  { field: 'in_iface', input: 'text', placeholder: 'eth1' },
  { field: 'src_mac', input: 'text', placeholder: 'aa:bb:cc:dd:ee:ff' },
  { field: 'proto', input: 'choice', choices: ['tcp', 'udp'] },
  { field: 'dst_ip', input: 'text', placeholder: '10.0.0.0/8' },
  { field: 'dst_port', input: 'text', placeholder: '443, 8000-9000' },
  { field: 'dst_domain', input: 'text', placeholder: 'example.com, =exact.com, ~keyword' },
  { field: 'dst_geo', input: 'text', placeholder: 'CN, JP' },
  { field: 'dst_asn', input: 'text', placeholder: '13335' },
];
export const fieldInfo = (f: CondField) => FIELDS.find((x) => x.field === f)!;

/** Values typed as one line: split on commas, spaces and new lines. */
export const parseValues = (s: string) => s.split(/[\s,]+/).map((v) => v.trim()).filter(Boolean);

/** One-line description of a condition, for lists and collapsed rows. */
export function describeCond(c: Cond | undefined, t: TFunction, names: { cond: Map<string, string>; user: Map<string, string> }): string {
  if (!c) return t('policy.always');
  switch (c.type) {
    case 'all':
    case 'any': {
      if (!c.items.length) return t('policy.always');
      const parts = c.items.map((x) => describeCond(x, t, names));
      return parts.length === 1 ? parts[0] : `(${parts.join(c.type === 'all' ? ` ${t('policy.and')} ` : ` ${t('policy.or')} `)})`;
    }
    case 'not': return `${t('policy.not')} ${describeCond(c.item, t, names)}`;
    case 'ref': return `[${names.cond.get(c.ref) || c.ref}]`;
    case 'match': {
      const vals = c.field === 'user' ? c.values.map((v) => names.user.get(v) || v) : c.field === 'src_kind' ? c.values.map((v) => t(`policy.kind.${v}`)) : c.values;
      const shown = vals.length > 3 ? `${vals.slice(0, 3).join(', ')} +${vals.length - 3}` : vals.join(', ');
      return `${t(`policy.field.${c.field}`)}: ${shown || '—'}`;
    }
  }
}

/** One-line description of a terminal. */
export function describeTerminal(x: Terminal, t: TFunction, policies: Map<string, string>): string {
  switch (x.type) {
    case 'deny': return x.reject === false ? t('policy.term.drop') : t('policy.term.reject');
    case 'exit': return `${t('policy.term.exit')}: ${x.paths.join(' | ') || '—'}`;
    case 'iface': return `${t('policy.term.iface')}: ${x.iface}${x.src ? ` (${x.src})` : ''}`;
    case 'call': return `${t('policy.term.call')}: ${policies.get(x.policy) || x.policy}`;
    case 'default': return t('policy.term.default');
  }
}

export type SetKind = 'policy' | 'condition';
export type AnySet = PolicySet | ConditionSet;
