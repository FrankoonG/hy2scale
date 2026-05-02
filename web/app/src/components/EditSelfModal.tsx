import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient, useQuery } from '@tanstack/react-query';
import { Modal, Button, Input, PasswordInput, Select, FormGroup, FormGrid, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import { useNodeStore } from '@/store/node';
import { buildHy2Url, canExportAsHy2Url, copyToClipboard } from '@/utils/buildHy2Url';

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
  // Share host for the export URL. listenIp is usually a wildcard
  // (`0.0.0.0` / `::`) which a remote client cannot dial, so we keep
  // a separate field. Defaults to the browser's current hostname —
  // which is, by definition, an address that just reached this node.
  const [shareHost, setShareHost] = useState('');

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

  useEffect(() => {
    if (open && node) {
      setNodeId(node.node_id || '');
      const listen = node.server?.listen || '0.0.0.0:5565';
      const match = listen.match(/^(.+):(.+)$/);
      const ip = match ? match[1] : '0.0.0.0';
      setListenIp(ip);
      setListenPort(match ? match[2] : '5565');
      setPassword(node.server?.password || '');
      // Default share host: bound IP if non-wildcard, else the browser's
      // current hostname (typed once by the operator to reach this UI,
      // therefore externally reachable from where they sit).
      const isWildcard = ip === '0.0.0.0' || ip === '::' || ip === '0:0:0:0:0:0:0:0';
      setShareHost(isWildcard ? (typeof window !== 'undefined' ? window.location.hostname : '') : ip);
      // Resolve tls_cert path to cert id: /data/tls/{id}.crt → id
      const certPath = node.server?.tls_cert || '';
      const certMatch = certPath.match(/\/data\/tls\/(.+)\.crt$/);
      setTlsCertId(certMatch ? certMatch[1] : '');
    }
  }, [open, node]);

  const handleExport = async () => {
    const host = shareHost.trim();
    const port = listenPort.trim();
    const addr = host && port ? `${host}:${port}` : '';
    if (!canExportAsHy2Url(addr)) {
      toast.error(t('nodes.exportNotShareable'));
      return;
    }
    if (!password) {
      toast.error(t('nodes.passRequired'));
      return;
    }
    let url: string;
    try {
      url = buildHy2Url({
        name: nodeId.trim() || node?.name || 'self',
        addr,
        password,
        // Self exports rarely want to advertise a custom SNI; let
        // the recipient set it if they need to.
      });
    } catch {
      toast.error(t('nodes.exportNotShareable'));
      return;
    }
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
      const listen = `${listenIp.trim() || '0.0.0.0'}:${listenPort.trim() || '5565'}`;
      const tls_cert = tlsCertId ? `/data/tls/${tlsCertId}.crt` : '';
      const tls_key = tlsCertId ? `/data/tls/${tlsCertId}.key` : '';
      await api.updateNode({
        node_id: nodeId.trim(),
        name: nodeId.trim(),
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

        <div className="section-divider">{t('nodes.hy2Server')}</div>

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

        <FormGroup label={t('nodes.shareHost')}>
          <Input
            value={shareHost}
            onChange={(e) => setShareHost(e.target.value)}
            placeholder={typeof window !== 'undefined' ? window.location.hostname : ''}
          />
        </FormGroup>

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
