import { api } from './client';

/** Condition sets / policy sets / direct entry (docs/v1.4.0-policy.md). */

export type CondField =
  | 'src_kind' | 'user' | 'src_node' | 'src_ip' | 'src_port' | 'in_iface' | 'src_mac'
  | 'proto' | 'dst_ip' | 'dst_port' | 'dst_domain' | 'dst_geo' | 'dst_asn';

export type Cond =
  | { type: 'all' | 'any'; items: Cond[] }
  | { type: 'not'; item: Cond }
  | { type: 'ref'; ref: string }
  | { type: 'match'; field: CondField; values: string[] };

export type Terminal =
  | { type: 'deny'; reject?: boolean }
  | { type: 'exit'; paths: string[]; mode?: '' | 'quality' | 'aggregate' }
  | { type: 'iface'; iface: string; src?: string }
  | { type: 'call'; policy: string }
  | { type: 'default' };

export interface CondRule { id: string; enabled: boolean; when?: Cond; result: boolean }
export interface PolicyRule { id: string; name?: string; enabled: boolean; when?: Cond; then: Terminal }

export interface SetRef { kind: 'user' | 'proxy' | 'wireguard' | 'vpn' | 'policy' | 'condition' | 'direct'; id: string; name: string }
export interface ConditionSet { id: string; name: string; rules: CondRule[]; refs?: SetRef[] }
export interface PolicySet { id: string; name: string; rules: PolicyRule[]; refs?: SetRef[]; warnings?: string[] }

export type OnStop = 'open' | 'block';
export interface SystemLocal { enabled: boolean; dns: boolean; on_stop: OnStop }
export interface SystemForward { enabled: boolean; interfaces: string[]; sources: string[]; dns: boolean; on_stop: OnStop }
export interface PolicySystem { local: SystemLocal; forward: SystemForward }

export interface PolicyConfig {
  max_depth: number;
  direct: string;
  system: PolicySystem;
  conditions: ConditionSet[];
  policies: PolicySet[];
  migrated_notice?: boolean;
}

export interface Avail { available: boolean; reason?: string }
export interface PolicyCapabilities {
  system: { local: Avail; forward: Avail & { ip_forward?: boolean } };
  heartbeat: Avail; iface_terminal: Avail; geoip: boolean; dns_proxy: Avail;
}
export interface PolicyIface { name: string; type: string; addrs: string[]; up: boolean; members?: string[] }
export interface PolicyStatus {
  local: { active: boolean; error?: string };
  forward: { active: boolean; error?: string };
  heartbeat?: { ok: boolean; last?: string };
}

export interface PolicyTestRequest {
  start?: string;
  source: { kind: string; user?: string; ip?: string; port?: number; iface?: string; mac?: string; node?: string };
  dest: { host?: string; ip?: string; port: number; proto: 'tcp' | 'udp' };
}
export interface PolicyTestResult {
  chain: { policy: string; policy_name: string; rule: string; rule_name?: string; depth: number }[];
  terminal: Terminal;
  reason?: string;
  fields?: Record<string, string>;
}

/** 400 body of a rejected set: the message and where in the set. */
export class PolicyValidationError extends Error {
  constructor(message: string, public path?: string, public refs?: SetRef[]) { super(message); }
}

async function send<T>(path: string, method: string, body?: unknown): Promise<T> {
  try {
    return await api<T>(path, { method, body: body === undefined ? undefined : JSON.stringify(body) });
  } catch (e: any) {
    // The API error carries the response text; validation answers JSON.
    try {
      const j = JSON.parse(e.message);
      throw new PolicyValidationError(j.error || e.message, j.path, j.refs);
    } catch (pe) {
      if (pe instanceof PolicyValidationError) throw pe;
      throw e;
    }
  }
}

export const getPolicy = () => api<PolicyConfig>('/policy');
export const updatePolicySettings = (data: Partial<Pick<PolicyConfig, 'max_depth' | 'direct' | 'system'>>) =>
  send<PolicyConfig>('/policy/settings', 'PUT', data);
export const dismissPolicyNotice = () => send('/policy/notice/dismiss', 'POST');

export const createCondition = (s: Omit<ConditionSet, 'id' | 'refs'>) => send<ConditionSet>('/policy/conditions', 'POST', s);
export const updateCondition = (s: ConditionSet) => send<ConditionSet>(`/policy/conditions/${encodeURIComponent(s.id)}`, 'PUT', s);
export const deleteCondition = (id: string) => send(`/policy/conditions/${encodeURIComponent(id)}`, 'DELETE');

export const createPolicySet = (s: Omit<PolicySet, 'id' | 'refs' | 'warnings'>) => send<PolicySet>('/policy/policies', 'POST', s);
export const updatePolicySet = (s: PolicySet) => send<PolicySet>(`/policy/policies/${encodeURIComponent(s.id)}`, 'PUT', s);
export const deletePolicySet = (id: string) => send(`/policy/policies/${encodeURIComponent(id)}`, 'DELETE');

export const getPolicyCapabilities = () => api<PolicyCapabilities>('/policy/capabilities');
export const getPolicyInterfaces = () => api<PolicyIface[]>('/policy/interfaces');
export const getPolicyStatus = () => api<PolicyStatus>('/policy/status');
export const testPolicy = (req: PolicyTestRequest) => send<PolicyTestResult>('/policy/test', 'POST', req);
