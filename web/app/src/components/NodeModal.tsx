import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import {
  Modal, Button, Input, PasswordInput, Toggle, Textarea,
  FormGroup, FormGrid, useToast,
} from '@hy2scale/ui';
import * as api from '@/api';
import type { ClientEntry } from '@/api';

interface Props {
  open: boolean;
  onClose: () => void;
  editingName: string | null;
  animateFrom?: { x: number; y: number };
}

export default function NodeModal({ open, onClose, editingName, animateFrom }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();

  const [loading, setLoading] = useState(false);
  const [addr, setAddr] = useState('');
  const [addrs, setAddrs] = useState<string[]>([]);
  const [password, setPassword] = useState('');
  const [sni, setSni] = useState('');
  const [insecure, setInsecure] = useState(false);
  const [ca, setCa] = useState('');
  const [maxTx, setMaxTx] = useState('');
  const [maxRx, setMaxRx] = useState('');
  const [fastOpen, setFastOpen] = useState(false);
  const [connMode, setConnMode] = useState<'' | 'quality' | 'aggregate'>('');
  const [initStreamWin, setInitStreamWin] = useState('');
  const [maxStreamWin, setMaxStreamWin] = useState('');
  const [initConnWin, setInitConnWin] = useState('');
  const [maxConnWin, setMaxConnWin] = useState('');

  // Load existing data when editing
  useEffect(() => {
    if (!open) return;
    if (!editingName) {
      // Reset for add
      setAddr(''); setAddrs([]); setPassword(''); setSni(''); setInsecure(false);
      setCa(''); setMaxTx(''); setMaxRx(''); setFastOpen(false); setConnMode('');
      setInitStreamWin(''); setMaxStreamWin(''); setInitConnWin(''); setMaxConnWin('');
      return;
    }
    // Fetch client data
    api.getClients().then((clients) => {
      const c = clients.find((cl) => cl.name === editingName);
      if (!c) return;
      setAddr(c.addr || '');
      setAddrs(c.addrs || []);
      setPassword(c.password || '');
      setSni(c.sni || '');
      setInsecure(c.insecure || false);
      setCa(c.ca || '');
      setMaxTx(c.max_tx ? String(c.max_tx / 125000) : '');
      setMaxRx(c.max_rx ? String(c.max_rx / 125000) : '');
      setFastOpen(c.fast_open || false);
      setConnMode(c.conn_mode || '');
      setInitStreamWin(c.init_stream_window ? String(c.init_stream_window) : '');
      setMaxStreamWin(c.max_stream_window ? String(c.max_stream_window) : '');
      setInitConnWin(c.init_conn_window ? String(c.init_conn_window) : '');
      setMaxConnWin(c.max_conn_window ? String(c.max_conn_window) : '');
    });
  }, [open, editingName]);

  const handleSubmit = async () => {
    const allAddrs = addrs.length > 0 ? addrs : (addr ? [addr] : []);
    if (allAddrs.length === 0 || !password) {
      toast.error(t('nodes.addrPassRequired'));
      return;
    }

    setLoading(true);
    const data: ClientEntry = {
      name: editingName || '',
      addr: allAddrs[0],
      addrs: allAddrs.length > 1 ? allAddrs : undefined,
      password,
      sni: sni || undefined,
      insecure: insecure || undefined,
      ca: ca || undefined,
      max_tx: maxTx ? parseInt(maxTx) * 125000 : undefined,
      max_rx: maxRx ? parseInt(maxRx) * 125000 : undefined,
      fast_open: fastOpen || undefined,
      conn_mode: connMode || undefined,
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
        toast.success(t('nodes.connectedTo', { name: data.addr }));
      }
      queryClient.invalidateQueries({ queryKey: ['topology'] });
      onClose();
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  const addAddress = () => {
    if (addrs.length === 0 && addr) setAddrs([addr, '']);
    else setAddrs([...addrs, '']);
  };

  const updateAddr = (i: number, val: string) => {
    const a = [...addrs]; a[i] = val; setAddrs(a);
    if (i === 0) setAddr(val);
  };

  const removeAddr = (i: number) => {
    const a = addrs.filter((_, idx) => idx !== i);
    setAddrs(a);
    if (a.length > 0) setAddr(a[0]);
  };

  const title = editingName ? t('nodes.editPrefix', { name: editingName }) : t('nodes.addTitle');

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
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        {/* Addresses */}
        <FormGroup label={t('nodes.addresses')} required>
          {(addrs.length > 0 ? addrs : [addr]).map((a, i) => (
            <div key={i} style={{ display: 'flex', gap: 6, marginBottom: 6 }}>
              <Input
                value={a}
                onChange={(e) => addrs.length > 0 ? updateAddr(i, e.target.value) : setAddr(e.target.value)}
                placeholder="host:port"
                style={{ flex: 1 }}
              />
              {addrs.length > 1 && (
                <button className="hy-icon-btn danger" onClick={() => removeAddr(i)}>✕</button>
              )}
            </div>
          ))}
          <Button size="sm" variant="ghost" onClick={addAddress}>{t('nodes.addAddress')}</Button>
        </FormGroup>

        <FormGroup label={t('nodes.password')} required>
          <PasswordInput value={password} onChange={(e) => setPassword(e.target.value)} />
        </FormGroup>

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
          <Textarea
            value={ca}
            onChange={(e) => setCa(e.target.value)}
            rows={3}
            monospace
            placeholder="-----BEGIN CERTIFICATE-----"
          />
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

        <FormGroup label={t('nodes.fastOpen')}>
          <Toggle checked={fastOpen} onChange={(e) => setFastOpen(e.target.checked)} />
        </FormGroup>

        {/* QUIC Advanced */}
        <details>
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
