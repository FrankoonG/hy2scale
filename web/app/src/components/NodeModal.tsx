import { useState, useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient, useQuery } from '@tanstack/react-query';
import { Reorder, useDragControls } from 'framer-motion';
import {
  Modal, Button, Input, PasswordInput, Toggle, Textarea, Select,
  FormGroup, FormGrid, Tabs, TabPanel, GripIcon, useToast,
} from '@hy2scale/ui';
import clsx from 'clsx';
import * as api from '@/api';
import type { ClientEntry, CertInfo } from '@/api';

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

  const [tab, setTab] = useState('addrs');
  const [loading, setLoading] = useState(false);
  const [addrItems, setAddrItems] = useState<AddrItem[]>([{ id: addrNextId++, host: '', port: '' }]);
  const addrListRef = useRef<HTMLUListElement>(null);
  const [connMode, setConnMode] = useState<'' | 'quality' | 'aggregate'>('');
  const [addrError, setAddrError] = useState('');

  // Connection tab
  const [password, setPassword] = useState('');
  const [fastOpen, setFastOpen] = useState(false);
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

  // Load CA certs for selector
  const { data: certs } = useQuery({
    queryKey: ['certs'],
    queryFn: () => api.getCerts(),
    enabled: open,
  });
  const caCerts = (certs || []).filter((c: CertInfo) => c.is_ca);

  // Reset tab on close so next open starts fresh
  useEffect(() => {
    if (!open) {
      setTab('addrs');
      setAddrError('');
      return;
    }
    setAddrError('');
    if (!editingName) {
      // Reset for add
      setAddrItems([{ id: addrNextId++, host: '', port: '' }]);
      setConnMode('');
      setPassword(''); setFastOpen(false);
      setMaxTx(''); setMaxRx('');
      setSni(''); setInsecure(true);
      setCaSource(''); setCaManual('');
      setInitStreamWin(''); setMaxStreamWin('');
      setInitConnWin(''); setMaxConnWin('');
      setShowQuic(false);
      return;
    }
    // Fetch client data
    api.getClients().then((clients) => {
      const c = clients.find((cl) => cl.name === editingName);
      if (!c) return;
      const addrs = c.addrs && c.addrs.length ? c.addrs : (c.addr ? [c.addr] : ['']);
      setAddrItems(addrs.map(a => { const p = parseAddr(a); return { id: addrNextId++, host: p.host, port: p.port }; }));
      setConnMode(c.conn_mode || '');
      setPassword(c.password || '');
      setFastOpen(c.fast_open || false);
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
  }, [open, editingName]);

  // Sync connection mode when address count changes
  useEffect(() => {
    const count = addrItems.length;
    if (count <= 1) {
      setConnMode('');
    } else if (connMode === '') {
      setConnMode('quality');
    }
  }, [addrItems.length]);

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
      if (!item.host.trim()) {
        setAddrError(t('nodes.hostRequired'));
        setTab('addrs');
        return null;
      }
      if (!item.port.trim() || !validatePortSpec(item.port.trim())) {
        setAddrError(t('nodes.invalidPort'));
        setTab('addrs');
        return null;
      }
    }
    const strs = addrItems.map(r => `${r.host.trim()}:${r.port.trim()}`);
    const seen = new Set<string>();
    for (const s of strs) {
      if (seen.has(s)) {
        setAddrError(t('nodes.dupAddress', { addr: s }));
        setTab('addrs');
        return null;
      }
      seen.add(s);
    }
    return strs;
  };

  const handleSubmit = async () => {
    const addrs = validateAddrs();
    if (!addrs) return;
    if (!password.trim()) {
      toast.error(t('nodes.passRequired'));
      setTab('conn');
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
      name: editingName || addrs[0],
      addr: addrs[0],
      addrs: addrs.length > 1 ? addrs : undefined,
      password: password.trim(),
      conn_mode: connMode || undefined,
      sni: sni.trim() || undefined,
      insecure: insecure || undefined,
      ca: caVal,
      max_tx: maxTx ? Math.round(parseFloat(maxTx) * 125000) : undefined,
      max_rx: maxRx ? Math.round(parseFloat(maxRx) * 125000) : undefined,
      fast_open: fastOpen || undefined,
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
  const hasMultiAddr = addrItems.length > 1;

  // Build CA select options
  const caOptions = [
    { value: '', label: t('nodes.caNone') },
    ...caCerts.map(c => ({ value: c.id, label: c.name || c.id })),
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
          <Button variant="primary" onClick={handleSubmit} loading={loading}>
            {editingName ? t('app.save') : t('nodes.connect')}
          </Button>
        </>
      }
    >
      <Tabs
        items={[
          { key: 'addrs', label: t('nodes.addresses') },
          { key: 'conn', label: t('nodes.connection') },
        ]}
        activeKey={tab}
        onChange={setTab}
      />

      <TabPanel activeKey={tab} keys={['addrs', 'conn']}>
        {tab === 'addrs' ? (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
            {/* Connection Mode */}
            <div className={clsx('exit-mode-options', !hasMultiAddr && 'exit-mode-disabled')}>
              <label className="exit-mode-opt">
                <input
                  type="radio"
                  name="connMode"
                  checked={connMode === ''}
                  onChange={() => setConnMode('')}
                  disabled={hasMultiAddr}
                />
                {t('exit.modeNone')}
              </label>
              <label className="exit-mode-opt">
                <input
                  type="radio"
                  name="connMode"
                  checked={connMode === 'quality'}
                  onChange={() => setConnMode('quality')}
                  disabled={!hasMultiAddr}
                />
                {t('exit.modeStability')}
              </label>
              <label className="exit-mode-opt">
                <input
                  type="radio"
                  name="connMode"
                  checked={connMode === 'aggregate'}
                  onChange={() => setConnMode('aggregate')}
                  disabled={!hasMultiAddr}
                />
                {t('exit.modeSpeed')}
              </label>
            </div>

            {/* Address rows */}
            {addrError && <div style={{ color: 'var(--red)', fontSize: 13 }}>{addrError}</div>}
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
          </div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
            <FormGroup label={t('nodes.password')} required>
              <PasswordInput value={password} onChange={(e) => setPassword(e.target.value)} />
            </FormGroup>

            <FormGroup label={t('nodes.fastOpen')}>
              <Toggle checked={fastOpen} onChange={(e) => setFastOpen(e.target.checked)} />
            </FormGroup>

            {/* Bandwidth */}
            <FormGrid>
              <FormGroup label={t('nodes.upload')}>
                <Input type="number" value={maxTx} onChange={(e) => setMaxTx(e.target.value)} placeholder="0" suffix="Mbps" />
              </FormGroup>
              <FormGroup label={t('nodes.download')}>
                <Input type="number" value={maxRx} onChange={(e) => setMaxRx(e.target.value)} placeholder="0" suffix="Mbps" />
              </FormGroup>
            </FormGrid>

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
        )}
      </TabPanel>
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
