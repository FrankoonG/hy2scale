import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient, useQuery } from '@tanstack/react-query';
import { Modal, Button, Input, PasswordInput, Select, Toggle, FormGroup, FormGrid, useToast, SectionDivider } from '@hy2scale/ui';
import * as api from '@/api';
import { useNodeStore } from '@/store/node';
import { useDialogInit } from '@/hooks/useFormInit';
import { canExportAsHy2Url, copyToClipboard } from '@/utils/buildHy2Url';
import { buildScaleLink } from '@/utils/nodeLink';

// IPv6 hosts are kept bare in the inputs ("::", "2001:db8::1") and
// bracketed only when joined with a port.
function bareHost(h: string): string {
  return h.trim().replace(/^\[(.*)\]$/, '$1');
}
function joinHostPort(h: string, port: string): string {
  const b = bareHost(h);
  return `${b.includes(':') ? `[${b}]` : b}:${port}`;
}
function isWildcardHost(h: string): boolean {
  const b = bareHost(h);
  return b === '' || b === '0.0.0.0' || (b.includes(':') && /^[0:]+$/.test(b));
}

interface Props {
  open: boolean;
  onClose: () => void;
  animateFrom?: { x: number; y: number };
}

export default function EditSelfModal({ open, onClose, animateFrom }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();
  const node = useNodeStore((s) => s.node);

  const [loading, setLoading] = useState(false);
  const [nodeId, setNodeId] = useState('');
  const [listenIp, setListenIp] = useState('');
  const [listenPort, setListenPort] = useState('');
  const [password, setPassword] = useState('');
  const [tlsCertId, setTlsCertId] = useState('');
  // Share host for the exported link; empty = this node's public egress
  // addresses (never the browser's host: through a remote session that is
  // the managing node's).
  const [shareHost, setShareHost] = useState('');
  const topology = useNodeStore((s) => s.topology);
  const self = topology.find((n) => n.is_self);
  const publicHosts = [self?.egress?.ipv4, self?.egress?.ipv6].filter((f) => f?.ok && f.ip).map((f) => f!.ip);
  const { data: settings } = useQuery({ queryKey: ['settings'], queryFn: api.getUISettings, enabled: open });
  // Exported URL: skip certificate verification by default — a node without
  // a TLS certificate from the TLS page serves a self-signed one.
  const [exportInsecure, setExportInsecure] = useState(true);

  // Fetch TLS certs for dropdown
  const { data: certs } = useQuery({
    queryKey: ['tls'],
    queryFn: () => api.getCerts(),
    enabled: open,
  });

  // Need the user list to detect a hy2-server / user-account password
  // collision before submitting — peers receive the server password in
  // cleartext via the clients[] block, so a duplicated user password
  // would leak that user's credentials cluster-wide.
  const { data: users } = useQuery({
    queryKey: ['users'],
    queryFn: () => api.getUsers(),
    enabled: open,
  });

  // Seed on open (or once node info arrives) — not on every ['node'] refetch.
  useDialogInit(open, node ? 'ready' : null, () => {
    setExportInsecure(true);
    if (node) {
      setNodeId(node.node_id || '');
      const listen = node.server?.listen || '0.0.0.0:5565';
      const match = listen.match(/^(.+):(.+)$/);
      const ip = match ? bareHost(match[1]) : '0.0.0.0';
      setListenIp(ip);
      setListenPort(match ? match[2] : '5565');
      setPassword(node.server?.password || '');
      // a specific bound address is the share host; otherwise the public addresses
      setShareHost(isWildcardHost(ip) ? '' : ip);
      // tls_cert is a path in the node's TLS store: <data>/tls/{id}.crt → id
      const certPath = node.server?.tls_cert || '';
      const certMatch = certPath.match(/\/tls\/([^/]+)\.crt$/);
      setTlsCertId(certMatch ? certMatch[1] : '');
    }
  });

  // Nodes this one is connected to: an importer can signal a punch through
  // them (only when this node accepts punched connections).
  const punchVia = () => {
    if (!settings?.accept_punch) return [];
    const direct = [...(self?.children || []), ...topology.filter((n) => !n.is_self)];
    return [...new Set(direct.filter((n) => n.connected && !n.native && !n.disabled).map((n) => n.name))].slice(0, 8);
  };

  const handleExport = async () => {
    const port = listenPort.trim();
    const hosts = shareHost.trim() ? [shareHost.trim()] : publicHosts;
    const addrs = port ? hosts.map((h) => joinHostPort(h, port)).filter((a) => canExportAsHy2Url(a)) : [];
    const via = punchVia();
    if (!addrs.length && !via.length) {
      toast.error(t('nodes.exportNotShareable'));
      return;
    }
    if (!password) {
      toast.error(t('nodes.passRequired'));
      return;
    }
    const url = buildScaleLink({
      name: nodeId.trim() || node?.name || 'self',
      addrs,
      password,
      insecure: exportInsecure || undefined,
      punch: via.length ? { via } : undefined,
    });
    const ok = await copyToClipboard(url);
    if (ok) toast.success(t('nodes.exportCopied'));
    else toast.error(t('nodes.exportFailed'));
  };

  const handleSave = async () => {
    if (!nodeId.trim()) {
      toast.error(t('nodes.nodeIdRequired'));
      return;
    }
    if (password) {
      const collide = (users || []).find((u) => u.password === password);
      if (collide) {
        toast.error(t('nodes.serverPasswordCollidesUser', { name: collide.username }));
        return;
      }
    }
    setLoading(true);
    try {
      const listen = joinHostPort(listenIp.trim() || '0.0.0.0', listenPort.trim() || '5565');
      // The store ID; the node maps it to its own TLS directory.
      const tls_cert = tlsCertId || '';
      const tls_key = '';
      await api.updateNode({
        node_id: nodeId.trim(),
        server: { listen, password, tls_cert, tls_key },
      });
      toast.success(t('nodes.settingsSaved'));
      queryClient.invalidateQueries({ queryKey: ['node'] });
      queryClient.invalidateQueries({ queryKey: ['topology'] });
      onClose();
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  // Build TLS cert options: self-signed (auto) + certs with private key
  const tlsOptions = [
    { value: '', label: t('nodes.selfSignedAuto') },
    ...(certs || [])
      .filter((c) => !!c.key_file)
      .map((c) => ({ value: c.id, label: `${c.name} (${c.subject})` })),
  ];

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('nodes.editSelf')}
      animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          <Button onClick={handleExport} data-testid="export-self-hy2-url">
            {t('nodes.exportUrl')}
          </Button>
          <Button variant="primary" onClick={handleSave} loading={loading}>{t('app.save')}</Button>
        </>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <FormGroup label={t('settings.nodeId')} required>
          <Input value={nodeId} onChange={(e) => setNodeId(e.target.value)} />
        </FormGroup>

        <SectionDivider>{t('nodes.hy2Server')}</SectionDivider>

        <FormGroup label={t('nodes.listenUdp')}>
          <div style={{ display: 'flex', gap: 8 }}>
            <Input
              value={listenIp}
              onChange={(e) => setListenIp(e.target.value)}
              placeholder="0.0.0.0"
              style={{ flex: 3 }}
            />
            <Input
              value={listenPort}
              onChange={(e) => setListenPort(e.target.value)}
              placeholder="5565 or 5000-6000"
              style={{ flex: 2 }}
            />
          </div>
        </FormGroup>

        <FormGroup label={t('nodes.password')}>
          <PasswordInput
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            onGenerate={setPassword}
          />
        </FormGroup>

        <FormGrid>
          <FormGroup label={t('nodes.shareHost')}>
            <Input
              value={shareHost}
              onChange={(e) => setShareHost(e.target.value)}
              placeholder={publicHosts.join(', ') || t('nodes.shareHostNone')}
              data-testid="share-host"
            />
          </FormGroup>
          <FormGroup label={t('nodes.skipVerify')}>
            <div style={{ paddingTop: 6 }}>
              <Toggle checked={exportInsecure} onChange={(e) => setExportInsecure(e.target.checked)} />
            </div>
          </FormGroup>
        </FormGrid>

        <FormGroup label={t('settings.tlsCert')}>
          <Select
            value={tlsCertId}
            onChange={(e) => setTlsCertId(e.target.value)}
            options={tlsOptions}
          />
        </FormGroup>
      </div>
    </Modal>
  );
}
