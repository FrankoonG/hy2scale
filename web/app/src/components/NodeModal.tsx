import { useState, useEffect, useMemo, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient, useQuery } from '@tanstack/react-query';
import { Reorder, useDragControls } from 'framer-motion';
import {
  Modal, Button, Input, PasswordInput, Toggle, Textarea, Select,
  FormGroup, FormGrid, GripIcon, useToast,
} from '@hy2scale/ui';
import * as api from '@/api';
import type { ClientEntry, CertInfo, TopologyNode } from '@/api';
import { useNodeStore } from '@/store/node';
import { buildHy2Url, canExportAsHy2Url, copyToClipboard } from '@/utils/buildHy2Url';

// Characters that would corrupt nested qualified paths (`/`), look weird in
// topology display (whitespace), or upset YAML/CLI consumers (control chars).
// The set is intentionally tight — anything matching makes the form refuse.
const NAME_BANNED_RE = /[\/\\\s\x00-\x1f\x7f]/;
function nameIsValid(s: string): boolean {
  return !!s && !NAME_BANNED_RE.test(s);
}

interface AddrItem { id: number; host: string; port: string; }
let addrNextId = 1;

interface Props {
  open: boolean;
  onClose: () => void;
  editingName: string | null;
  animateFrom?: { x: number; y: number };
}

interface AddrRow {
  host: string;
  port: string;
}

function validatePortSpec(spec: string): boolean {
  if (!spec) return false;
  const parts = spec.split(',').map(s => s.trim()).filter(Boolean);
  if (!parts.length) return false;
  for (const p of parts) {
    if (p.includes('-')) {
      const [a, b] = p.split('-').map(s => parseInt(s.trim()));
      if (isNaN(a) || isNaN(b) || a < 1 || b > 65535 || a > b) return false;
    } else {
      const n = parseInt(p);
      if (isNaN(n) || n < 1 || n > 65535) return false;
    }
  }
  return true;
}

function parseAddr(a: string): AddrRow {
  const m = a.match(/^(.+):(.+)$/);
  return m ? { host: m[1], port: m[2] } : { host: a, port: '' };
}

export default function NodeModal({ open, onClose, editingName, animateFrom }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();

  const [loading, setLoading] = useState(false);
  const [name, setName] = useState('');
  const [nameError, setNameError] = useState('');
  const [addrItems, setAddrItems] = useState<AddrItem[]>([{ id: addrNextId++, host: '', port: '' }]);
  const addrListRef = useRef<HTMLUListElement>(null);
  const [addrError, setAddrError] = useState('');

  // Live topology lookup — drives the disabled state of the name field.
  // Renaming a hy2scale peer mid-connection would orphan its keyed QUIC
  // session, so we only allow edits when the peer is offline OR is a
  // native (vanilla Hysteria 2) server (where the local name is just our
  // private label and the remote doesn't care).
  const topology = useNodeStore((s) => s.topology);
  const editingTopologyNode: TopologyNode | undefined = useMemo(() => {
    if (!editingName) return undefined;
    return topology.find((n) => n.name === editingName);
  }, [topology, editingName]);
  const editingConnected = editingTopologyNode?.connected === true;
  const editingNative = editingTopologyNode?.native === true;
  const nameLocked = !!editingName && editingConnected && !editingNative;

  const [password, setPassword] = useState('');
  const [fastOpen, setFastOpen] = useState(false);
  // Brutal congestion control: ON = client honours an explicit
  // upload/download ceiling (max_tx / max_rx). OFF = let the server
  // decide, and don't bother showing the ceiling inputs at all.
  const [brutal, setBrutal] = useState(false);
  const [bbrProfile, setBbrProfile] = useState<'' | 'standard' | 'conservative' | 'aggressive'>('');
  const [maxTx, setMaxTx] = useState('');
  const [maxRx, setMaxRx] = useState('');
  const [sni, setSni] = useState('');
  const [insecure, setInsecure] = useState(false);
  const [caSource, setCaSource] = useState<string>(''); // '' = none, cert id, or '__manual__'
  const [caManual, setCaManual] = useState('');
  const [initStreamWin, setInitStreamWin] = useState('');
  const [maxStreamWin, setMaxStreamWin] = useState('');
  const [initConnWin, setInitConnWin] = useState('');
  const [maxConnWin, setMaxConnWin] = useState('');
  const [showQuic, setShowQuic] = useState(false);

  // Load TLS certs for the CA-pinning selector. The "CA" field on a peer
  // entry is used to validate that peer's TLS handshake — common cases:
  //   * the peer serves a real CA-signed cert and we pin its CA;
  //   * the peer serves a self-signed cert (e.g. each hy2scale node's
  //     auto-generated `default` cert) and we pin THAT leaf as the trust
  //     anchor.
  // The first case wants `is_ca=true`. The second is a self-signed leaf
  // (`is_ca=false`) that the operator imported into TLS specifically so
  // they can pick it from the dropdown instead of re-pasting PEM into
  // every peer that talks to that node. Dropping the is_ca filter lets
  // both flows reach the dropdown — operators are choosing between
  // "trust this exact cert" and "trust certs signed by this CA".
  const { data: certs } = useQuery({
    queryKey: ['certs'],
    queryFn: () => api.getCerts(),
    enabled: open,
  });
  const caCerts = (certs || []) as CertInfo[];

  useEffect(() => {
    if (!open) {
      setAddrError('');
      setNameError('');
      return;
    }
    setAddrError('');
    setNameError('');
    // Reset every field to defaults synchronously, regardless of whether
    // we're adding or editing. For edit, the in-flight getClients() will
    // overwrite with the target's values once it resolves; until then the
    // form is blank instead of showing the previous edit target's
    // exit_via / password / etc. Without this, switching from peer A to
    // peer B would briefly render A's data — the bug pattern this fixes
    // also showed up in UserModal's exit_via column.
    setName(editingName || '');
    setAddrItems([{ id: addrNextId++, host: '', port: '' }]);
    setPassword(''); setFastOpen(false); setBrutal(false); setBbrProfile('');
    setMaxTx(''); setMaxRx('');
    setSni(''); setInsecure(true);
    setCaSource(''); setCaManual('');
    setInitStreamWin(''); setMaxStreamWin('');
    setInitConnWin(''); setMaxConnWin('');
    setShowQuic(false);
    if (!editingName) {
      return;
    }
    // cancelled flag closes over per-effect-run scope; when the
    // operator switches edit targets mid-fetch, the previous run's
    // cleanup sets it true so its late-resolving response is dropped
    // instead of clobbering the new target's just-rendered defaults.
    let cancelled = false;
    api.getClients().then((clients) => {
      if (cancelled) return;
      const c = clients.find((cl) => cl.name === editingName);
      if (!c) return;
      const addrs = c.addrs && c.addrs.length ? c.addrs : (c.addr ? [c.addr] : ['']);
      setAddrItems(addrs.map(a => { const p = parseAddr(a); return { id: addrNextId++, host: p.host, port: p.port }; }));
      setPassword(c.password || '');
      setFastOpen(c.fast_open || false);
      setBrutal(!!(c.max_tx || c.max_rx));
      setBbrProfile((c.bbr_profile as any) || '');
      setMaxTx(c.max_tx ? String(c.max_tx / 125000) : '');
      setMaxRx(c.max_rx ? String(c.max_rx / 125000) : '');
      setSni(c.sni || '');
      setInsecure(c.insecure || false);
      // Determine CA source
      if (c.ca) {
        // Check if it matches a known cert ID (starts with no dash = ID; PEM starts with -----)
        if (c.ca.startsWith('-----')) {
          setCaSource('__manual__');
          setCaManual(c.ca);
        } else {
          setCaSource(c.ca);
          setCaManual('');
        }
      } else {
        setCaSource('');
        setCaManual('');
      }
      setInitStreamWin(c.init_stream_window ? String(c.init_stream_window) : '');
      setMaxStreamWin(c.max_stream_window ? String(c.max_stream_window) : '');
      setInitConnWin(c.init_conn_window ? String(c.init_conn_window) : '');
      setMaxConnWin(c.max_conn_window ? String(c.max_conn_window) : '');
      const hasQuic = !!(c.init_stream_window || c.max_stream_window || c.init_conn_window || c.max_conn_window);
      setShowQuic(hasQuic);
    });
    return () => { cancelled = true; };
  }, [open, editingName]);

  // Sync connection mode when address count changes
  const addAddrRow = () => {
    setAddrItems([...addrItems, { id: addrNextId++, host: '', port: '' }]);
  };

  const updateAddrItem = (id: number, field: 'host' | 'port', val: string) => {
    setAddrItems(prev => prev.map(it => it.id === id ? { ...it, [field]: val } : it));
  };

  const removeAddrItem = (id: number) => {
    setAddrItems(prev => prev.filter(it => it.id !== id));
  };

  const validateAddrs = (): string[] | null => {
    setAddrError('');
    for (const item of addrItems) {
      const host = item.host.trim();
      if (!host) {
        setAddrError(t('nodes.hostRequired'));
        return null;
      }
      if (!item.port.trim() || !validatePortSpec(item.port.trim())) {
        setAddrError(t('nodes.invalidPort'));
        return null;
      }
      // IPv6 address validation: bracketed (`[fe80::1]`) or bare. Reject
      // anything that looks malformed before it reaches the backend
      // (which uses net.SplitHostPort and would silently fail).
      const stripped = host.startsWith('[') && host.endsWith(']') ? host.slice(1, -1) : host;
      if (stripped.includes(':') && !/^[0-9a-fA-F:]+$/.test(stripped)) {
        setAddrError(t('nodes.invalidIPv6'));
        return null;
      }
    }
    const strs = addrItems.map(r => {
      const h = r.host.trim();
      // Wrap bare IPv6 in brackets so `<host>:<port>` parses unambiguously.
      const isV6 = h.includes(':') && !h.startsWith('[');
      const formatted = isV6 ? `[${h}]` : h;
      return `${formatted}:${r.port.trim()}`;
    });
    const seen = new Set<string>();
    for (const s of strs) {
      if (seen.has(s)) {
        setAddrError(t('nodes.dupAddress', { addr: s }));
        return null;
      }
      seen.add(s);
    }
    return strs;
  };

  // resolveName returns the final node name from the form, or null when
  // the input is invalid. Empty is allowed only on create (falls back to
  // the first address). Sets nameError as a side effect on rejection.
  const resolveName = (addrs: string[]): string | null => {
    setNameError('');
    const trimmed = name.trim();
    if (!trimmed) {
      if (editingName) {
        setNameError(t('nodes.nodeNameRequired'));
        return null;
      }
      return addrs[0];
    }
    if (!nameIsValid(trimmed)) {
      setNameError(t('nodes.nodeNameInvalid'));
      return null;
    }
    return trimmed;
  };

  const handleExport = async () => {
    const addrs = validateAddrs();
    if (!addrs) return;
    const finalName = resolveName(addrs);
    if (!finalName) return;
    if (!password.trim()) {
      toast.error(t('nodes.passRequired'));
      return;
    }
    // First address only — the share-URL format has no native multi-addr
    // slot. validateAddrs returned the formatted list (IPv6-bracketed).
    if (!canExportAsHy2Url(addrs[0])) {
      toast.error(t('nodes.exportNotShareable'));
      return;
    }
    let url: string;
    try {
      url = buildHy2Url({
        name: finalName,
        addr: addrs[0],
        password: password.trim(),
        sni: sni.trim() || undefined,
        insecure: insecure || undefined,
        max_tx: maxTx ? Math.round(parseFloat(maxTx) * 125000) : undefined,
        max_rx: maxRx ? Math.round(parseFloat(maxRx) * 125000) : undefined,
        fast_open: fastOpen || undefined,
      });
    } catch {
      toast.error(t('nodes.exportNotShareable'));
      return;
    }
    const ok = await copyToClipboard(url);
    if (ok) toast.success(t('nodes.exportCopied'));
    else toast.error(t('nodes.exportFailed'));
  };

  const handleSubmit = async () => {
    const addrs = validateAddrs();
    if (!addrs) return;
    const finalName = resolveName(addrs);
    if (!finalName) return;
    if (!password.trim()) {
      toast.error(t('nodes.passRequired'));
      return;
    }

    // Determine CA value
    let caVal: string | undefined;
    if (caSource === '__manual__') {
      caVal = caManual.trim() || undefined;
    } else if (caSource) {
      caVal = caSource;
    }

    setLoading(true);
    const data: ClientEntry = {
      name: finalName,
      addr: addrs[0],
      addrs: addrs.length > 1 ? addrs : undefined,
      password: password.trim(),
      sni: sni.trim() || undefined,
      insecure: insecure || undefined,
      ca: caVal,
      max_tx: maxTx ? Math.round(parseFloat(maxTx) * 125000) : undefined,
      max_rx: maxRx ? Math.round(parseFloat(maxRx) * 125000) : undefined,
      fast_open: fastOpen || undefined,
      bbr_profile: bbrProfile || undefined,
      init_stream_window: initStreamWin ? parseInt(initStreamWin) : undefined,
      max_stream_window: maxStreamWin ? parseInt(maxStreamWin) : undefined,
      init_conn_window: initConnWin ? parseInt(initConnWin) : undefined,
      max_conn_window: maxConnWin ? parseInt(maxConnWin) : undefined,
    } as any;

    try {
      if (editingName) {
        await api.updateClient(editingName, data);
        toast.success(t('nodes.updated', { name: editingName }));
      } else {
        await api.createClient(data);
        toast.success(t('nodes.saved', { name: data.name }));
      }
      queryClient.invalidateQueries({ queryKey: ['topology'] });
      onClose();
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  const title = editingName ? t('nodes.editPrefix', { name: editingName }) : t('nodes.addTitle');

  // Build CA select options. Annotate each TLS-list entry with whether
  // it's a CA cert or a self-signed leaf so the operator picks the right
  // one — the dropdown otherwise looks identical for both, and a peer
  // pin against a CA cert vs a leaf cert validates very differently.
  const caOptions = [
    { value: '', label: t('nodes.caNone') },
    ...caCerts.map((c) => {
      const base = c.name || c.id;
      const tag = c.is_ca ? '[CA]' : '[cert]';
      return { value: c.id, label: `${base} ${tag}` };
    }),
    { value: '__manual__', label: t('nodes.caManual') },
  ];

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          {editingName && (
            <Button onClick={handleExport} data-testid="export-hy2-url">
              {t('nodes.exportUrl')}
            </Button>
          )}
          <Button variant="primary" onClick={handleSubmit} loading={loading}>
            {editingName ? t('app.save') : t('nodes.connect')}
          </Button>
        </>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        {/* Node name — full-width, above addresses. Disabled when editing
            an online hy2scale peer (renaming a live QUIC session would
            orphan its keyed state); always editable for native peers and
            for offline entries; on create, blank falls back to first addr. */}
        <FormGroup label={t('nodes.nodeName')}>
          <>
            {nameError && <div style={{ color: 'var(--red)', fontSize: 13, marginBottom: 6 }}>{nameError}</div>}
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={editingName ? '' : t('nodes.nodeNameHint')}
              disabled={nameLocked}
              error={!!nameError}
              data-testid="node-name-input"
              style={{ width: '100%' }}
            />
            {nameLocked && (
              <div style={{ marginTop: 6, fontSize: 12, color: 'var(--text-muted)' }}>
                {t('nodes.nodeNameLocked')}
              </div>
            )}
          </>
        </FormGroup>

        {/* Address rows */}
        <FormGroup label={t('nodes.addresses')}>
          <>
            {addrError && <div style={{ color: 'var(--red)', fontSize: 13, marginBottom: 6 }}>{addrError}</div>}
            <Reorder.Group
              ref={addrListRef}
              axis="y"
              values={addrItems}
              onReorder={setAddrItems}
              className="addr-list"
              style={{ listStyle: 'none', padding: 0, margin: 0 }}
            >
              {addrItems.map((item) => (
                <AddrRowItem
                  key={item.id}
                  item={item}
                  canDrag={addrItems.length > 1}
                  canRemove={addrItems.length > 1}
                  constraintsRef={addrListRef}
                  hostPlaceholder={t('nodes.host')}
                  portPlaceholder={t('nodes.port')}
                  deleteTitle={t('app.delete')}
                  onUpdate={(field, val) => updateAddrItem(item.id, field, val)}
                  onRemove={() => removeAddrItem(item.id)}
                />
              ))}
            </Reorder.Group>
            <div className="addr-add-row" onClick={addAddrRow}>
              {t('nodes.addAddress')}
            </div>
          </>
        </FormGroup>

        <FormGroup label={t('nodes.password')} required>
          <PasswordInput value={password} onChange={(e) => setPassword(e.target.value)} />
        </FormGroup>

        <FormGrid>
          {/* FastOpen + Brutal share the left cell so BBR Profile keeps
              its full half on the right. Each toggle carries its own
              inline label so it's clear which toggle is which. */}
          <FormGroup label={t('nodes.tuning')}>
            <div style={{ display: 'flex', gap: 14, paddingTop: 6, alignItems: 'center', flexWrap: 'wrap' }}>
              <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}>
                <Toggle checked={fastOpen} onChange={(e) => setFastOpen(e.target.checked)} />
                <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{t('nodes.fastOpen')}</span>
              </label>
              <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}>
                <Toggle
                  checked={brutal}
                  onChange={(e) => {
                    const on = e.target.checked;
                    setBrutal(on);
                    // Closing Brutal clears any stale ceilings so we don't
                    // silently send max_tx/max_rx the user can no longer see.
                    if (!on) { setMaxTx(''); setMaxRx(''); }
                  }}
                  data-testid="brutal-toggle"
                />
                <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{t('nodes.brutal')}</span>
              </label>
            </div>
          </FormGroup>
          <FormGroup label={t('nodes.bbrProfile')}>
            <Select
              value={bbrProfile}
              onChange={(e) => setBbrProfile(e.target.value as any)}
              options={[
                { value: '', label: t('nodes.bbrStandard') },
                { value: 'conservative', label: t('nodes.bbrConservative') },
                { value: 'aggressive', label: t('nodes.bbrAggressive') },
              ]}
            />
          </FormGroup>
        </FormGrid>

        {/* Bandwidth — only meaningful when Brutal is on; otherwise the
            server picks bandwidth and the inputs add noise. */}
        {brutal && (
          <FormGrid>
            <FormGroup label={t('nodes.upload')}>
              <Input type="number" value={maxTx} onChange={(e) => setMaxTx(e.target.value)} placeholder="0" suffix="Mbps" data-testid="brutal-up" />
            </FormGroup>
            <FormGroup label={t('nodes.download')}>
              <Input type="number" value={maxRx} onChange={(e) => setMaxRx(e.target.value)} placeholder="0" suffix="Mbps" data-testid="brutal-down" />
            </FormGroup>
          </FormGrid>
        )}

        {/* TLS */}
        <FormGrid>
          <FormGroup label={t('nodes.sni')}>
            <Input value={sni} onChange={(e) => setSni(e.target.value)} placeholder="server.example.com" />
          </FormGroup>
          <FormGroup label={t('nodes.skipVerify')}>
            <div style={{ paddingTop: 6 }}>
              <Toggle checked={insecure} onChange={(e) => setInsecure(e.target.checked)} />
            </div>
          </FormGroup>
        </FormGrid>

        <FormGroup label={t('nodes.caCert')}>
          <Select
            value={caSource}
            onChange={(e) => setCaSource(e.target.value)}
            options={caOptions}
          />
          {caSource === '__manual__' && (
            <Textarea
              value={caManual}
              onChange={(e) => setCaManual(e.target.value)}
              rows={3}
              monospace
              placeholder="-----BEGIN CERTIFICATE-----"
              style={{ marginTop: 8 }}
            />
          )}
        </FormGroup>

        {/* QUIC Advanced */}
        <details open={showQuic} onToggle={(e) => setShowQuic((e.target as HTMLDetailsElement).open)}>
          <summary style={{ cursor: 'pointer', fontSize: 13, color: 'var(--text-secondary)', marginBottom: 10 }}>
            {t('nodes.quicAdvanced')}
          </summary>
          <FormGrid>
            <FormGroup label={t('nodes.initStreamWindow')}>
              <Input type="number" value={initStreamWin} onChange={(e) => setInitStreamWin(e.target.value)} />
            </FormGroup>
            <FormGroup label={t('nodes.maxStreamWindow')}>
              <Input type="number" value={maxStreamWin} onChange={(e) => setMaxStreamWin(e.target.value)} />
            </FormGroup>
            <FormGroup label={t('nodes.initConnWindow')}>
              <Input type="number" value={initConnWin} onChange={(e) => setInitConnWin(e.target.value)} />
            </FormGroup>
            <FormGroup label={t('nodes.maxConnWindow')}>
              <Input type="number" value={maxConnWin} onChange={(e) => setMaxConnWin(e.target.value)} />
            </FormGroup>
          </FormGrid>
        </details>
      </div>
    </Modal>
  );
}

interface AddrRowItemProps {
  item: AddrItem;
  canDrag: boolean;
  canRemove: boolean;
  constraintsRef: React.RefObject<HTMLElement | null>;
  hostPlaceholder: string;
  portPlaceholder: string;
  deleteTitle: string;
  onUpdate: (field: 'host' | 'port', val: string) => void;
  onRemove: () => void;
}

function AddrRowItem({ item, canDrag, canRemove, constraintsRef, hostPlaceholder, portPlaceholder, deleteTitle, onUpdate, onRemove }: AddrRowItemProps) {
  const controls = useDragControls();
  return (
    <Reorder.Item
      value={item}
      dragListener={false}
      dragControls={controls}
      dragConstraints={constraintsRef}
      dragElastic={0.1}
      onDragStart={() => document.body.classList.add('dragging-active')}
      onDragEnd={() => document.body.classList.remove('dragging-active')}
      className="addr-row"
      style={{ listStyle: 'none' }}
    >
      {canDrag && (
        <div className="addr-drag" onPointerDown={(e) => controls.start(e)}>
          <GripIcon />
        </div>
      )}
      <Input
        value={item.host}
        onChange={(e) => onUpdate('host', e.target.value)}
        placeholder={hostPlaceholder}
        style={{ flex: 1 }}
      />
      <Input
        value={item.port}
        onChange={(e) => onUpdate('port', e.target.value)}
        placeholder={portPlaceholder}
        style={{ width: 140 }}
      />
      {canRemove && (
        <button type="button" className="addr-del" onClick={onRemove} title={deleteTitle}>−</button>
      )}
    </Reorder.Item>
  );
}
