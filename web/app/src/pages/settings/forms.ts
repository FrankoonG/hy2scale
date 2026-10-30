import { useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { useQueryClient, type UseQueryResult } from '@tanstack/react-query';
import { useToast } from '@hy2scale/ui';
import * as api from '@/api';
import type { UISettings } from '@/api';
import { getBasePath } from '@/api/client';
import { useServerFormInit } from '@/hooks/useFormInit';
import { signOut } from '@/hooks/authTransition';
import { sha256 } from '@/hooks/useAuth';
import { toListItems, fromListItems, type ListItem } from '@/components/OrderedList';
import { isPublicIPServer } from '@/utils/ipaddr';

// Form state lives in the page (not in each tab) so switching tabs keeps
// unsaved edits; each card seeds its own fields and re-seeds only after its
// own save, so saving one card never reverts another.

/** Server defaults of the System tab's numeric fields. */
export const SYSTEM_DEFAULTS = {
  max_nested_depth: 5,
  max_response_nodes: 1024,
  max_cache_entries: 5000,
  max_response_bytes: 1048576,
  max_fetch_fan_out: 8,
  path_loss_grace_s: 15,
  dns_resolver_cache_ttl: 300,
  dns_resolver_negative_ttl: 30,
  dns_resolver_cache_size: 1024,
  dns_resolver_query_timeout_ms: 3000,
} as const;
type NumKey = keyof typeof SYSTEM_DEFAULTS;
const NESTED_KEYS: NumKey[] = ['max_nested_depth', 'max_response_nodes', 'max_cache_entries', 'max_response_bytes', 'max_fetch_fan_out'];

export interface SystemValues extends Record<NumKey, string> {
  dns: string;
  relay_admin_passthrough: boolean;
  accept_punch: boolean;
  dns_resolver_enabled: boolean;
  public_method: 'public' | 'peer+public';
  public_interval: string;
  public_servers: ListItem[];
}

const numStrings = (s?: Partial<UISettings>) =>
  Object.fromEntries((Object.keys(SYSTEM_DEFAULTS) as NumKey[]).map((k) => [k, String((s?.[k] as number) || SYSTEM_DEFAULTS[k])])) as Record<NumKey, string>;

function systemFrom(s?: UISettings): SystemValues {
  const pip = s?.public_ip;
  return {
    ...numStrings(s),
    dns: s?.dns || '',
    relay_admin_passthrough: !!s?.relay_admin_passthrough,
    accept_punch: !!s?.accept_punch,
    dns_resolver_enabled: !!s?.dns_resolver_enabled,
    public_method: pip?.method === 'peer+public' ? 'peer+public' : 'public',
    public_interval: String(pip?.interval || 600),
    public_servers: toListItems(pip?.servers || []),
  };
}

/** System sub-pages, each saving only its own fields. */
export type SystemSection = 'network' | 'dns' | 'nested';
const SECTION_KEYS: Record<SystemSection, (keyof SystemValues)[]> = {
  network: ['relay_admin_passthrough', 'accept_punch', 'path_loss_grace_s', 'public_method', 'public_interval', 'public_servers'],
  dns: ['dns', 'dns_resolver_enabled', 'dns_resolver_cache_ttl', 'dns_resolver_negative_ttl', 'dns_resolver_cache_size', 'dns_resolver_query_timeout_ms'],
  nested: NESTED_KEYS,
};

const intOr = (v: string, k: NumKey) => parseInt(v) || SYSTEM_DEFAULTS[k];

/** Network, DNS and nested-discovery sub-pages: one state, per-section saves. */
export function useSystemForm(settingsQ: UseQueryResult<UISettings>) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [v, setV] = useState<SystemValues>(() => systemFrom());
  const [saving, setSaving] = useState<SystemSection | null>(null);
  // After a section's save, the refetch re-seeds only that section.
  const saved = useRef<SystemSection | null>(null);
  const resync = useServerFormInit(settingsQ, (s) => {
    const f = systemFrom(s);
    const sec = saved.current;
    saved.current = null;
    setV((p) => (sec ? { ...p, ...Object.fromEntries(SECTION_KEYS[sec].map((k) => [k, f[k]])) } : f));
  });

  const set = <K extends keyof SystemValues>(k: K, val: SystemValues[K]) => setV((p) => ({ ...p, [k]: val }));

  const payload = (sec: SystemSection): Partial<UISettings> | null => {
    if (sec === 'network') {
      const interval = Number(v.public_interval);
      if (!/^\d+$/.test(v.public_interval.trim()) || interval < 60) {
        toast.error(t('settings.publicIpIntervalInvalid'));
        return null;
      }
      const servers = fromListItems(v.public_servers);
      const badServer = servers.find((x) => !isPublicIPServer(x));
      if (badServer !== undefined) {
        toast.error(t('settings.publicIpServerInvalid', { v: badServer }));
        return null;
      }
      return {
        relay_admin_passthrough: v.relay_admin_passthrough,
        accept_punch: v.accept_punch,
        path_loss_grace_s: intOr(v.path_loss_grace_s, 'path_loss_grace_s'),
        public_ip: { method: v.public_method, interval, servers },
      };
    }
    if (sec === 'dns') {
      return {
        dns: v.dns,
        dns_resolver_enabled: v.dns_resolver_enabled,
        dns_resolver_cache_ttl: intOr(v.dns_resolver_cache_ttl, 'dns_resolver_cache_ttl'),
        dns_resolver_negative_ttl: intOr(v.dns_resolver_negative_ttl, 'dns_resolver_negative_ttl'),
        dns_resolver_cache_size: intOr(v.dns_resolver_cache_size, 'dns_resolver_cache_size'),
        dns_resolver_query_timeout_ms: intOr(v.dns_resolver_query_timeout_ms, 'dns_resolver_query_timeout_ms'),
      };
    }
    return Object.fromEntries(NESTED_KEYS.map((k) => [k, intOr(v[k], k)])) as Partial<UISettings>;
  };

  // All hot-reloadable: DNS (read at handshake time), nested limits, the DNS
  // resolver (cache purged on save; upstreams = `dns`), public IP, punch.
  const save = async (sec: SystemSection) => {
    const body = payload(sec);
    if (!body) return;
    setSaving(sec);
    try {
      const { public_ip, ...node } = body;
      await api.updateNodeSettings(node);
      if (public_ip) await api.updatePublicIPSettings(public_ip);
      toast.success(t('settings.savedHot'));
      saved.current = sec;
      resync();
      queryClient.invalidateQueries({ queryKey: ['settings'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setSaving(null); }
  };

  const restoreNestedDefaults = () =>
    setV((p) => ({ ...p, ...Object.fromEntries(NESTED_KEYS.map((k) => [k, String(SYSTEM_DEFAULTS[k])])) }));

  return { v, set, save, saving, restoreNestedDefaults };
}
export type SystemForm = ReturnType<typeof useSystemForm>;

/** Web UI card: listen, base path, HTTPS, session timeout. */
export function useWebForm(settingsQ: UseQueryResult<UISettings>) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [listen, setListen] = useState('');
  const [basePath, setBasePath] = useState('');
  const [forceHttps, setForceHttps] = useState(false);
  const [httpsCertId, setHttpsCertId] = useState('');
  const [sessionTimeout, setSessionTimeout] = useState('12');
  const [saving, setSaving] = useState(false);

  const resync = useServerFormInit(settingsQ, (s) => {
    setListen(s.listen || '');
    setBasePath(s.base_path || '');
    setForceHttps(s.force_https || false);
    setHttpsCertId(s.https_cert_id || '');
    setSessionTimeout(String(s.session_timeout_h || 12));
  });

  const save = async () => {
    if (forceHttps && !httpsCertId) {
      toast.error(t('settings.httpsCertRequired'));
      return;
    }
    setSaving(true);
    try {
      await api.updateWebSettings({
        listen, base_path: basePath, force_https: forceHttps,
        https_cert_id: httpsCertId, session_timeout_h: parseInt(sessionTimeout) || 12,
      });
      toast.success(t('settings.saved'));
      resync();
      queryClient.invalidateQueries({ queryKey: ['settings'] });
      // HTTPS applies immediately; move this tab over (not for /remote/ tabs,
      // whose origin is the proxying node).
      if (forceHttps && window.location.protocol === 'http:' && !getBasePath().includes('/remote/')) {
        window.location.href = `https://${window.location.host}${window.location.pathname}`;
      }
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setSaving(false); }
  };

  return {
    listen, setListen, basePath, setBasePath, forceHttps, setForceHttps,
    httpsCertId, setHttpsCertId, sessionTimeout, setSessionTimeout, save, saving,
  };
}
export type WebForm = ReturnType<typeof useWebForm>;

/** Change username / password (then log out). */
export function usePasswordForm() {
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useNavigate();
  const [curPw, setCurPw] = useState('');
  const [newUser, setNewUser] = useState('');
  const [newPw, setNewPw] = useState('');
  const [confirmPw, setConfirmPw] = useState('');
  const [saving, setSaving] = useState(false);

  const save = async () => {
    if (!curPw) { toast.error(t('settings.passwordRequired')); return; }
    if (!newUser && !newPw) { toast.error(t('settings.enterNewCreds')); return; }
    if (newPw && newPw !== confirmPw) { toast.error(t('error.passwordMismatch')); return; }
    setSaving(true);
    try {
      const curHash = await sha256(curPw);
      const newHash = newPw ? await sha256(newPw) : undefined;
      await api.changePassword({ current_password: curHash, new_username: newUser || undefined, new_password: newHash });
      toast.success(t('settings.passwordUpdated'));
      setTimeout(() => signOut(navigate), 2000);
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setSaving(false); }
  };

  return { curPw, setCurPw, newUser, setNewUser, newPw, setNewPw, confirmPw, setConfirmPw, save, saving };
}
export type PasswordForm = ReturnType<typeof usePasswordForm>;
