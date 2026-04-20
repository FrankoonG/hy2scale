import { useState, useCallback, type MouseEvent, type DragEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Card, Button, Badge, Tabs, Table, Modal, Input, Textarea, Select,
  FormGroup, FormGrid, TabPanel, useToast, useConfirm, useSelection, type Column,
} from '@hy2scale/ui';
import BulkActionBar from '@/components/BulkActionBar';
import * as api from '@/api';
import type { CertInfo } from '@/api';

const PEM_EXTS = ['.pem', '.crt', '.cer', '.key', '.pub', '.txt'];
const MAX_FILE = 64 * 1024;

function readPemFile(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    if (file.size > MAX_FILE) { reject('File too large'); return; }
    const ext = file.name.substring(file.name.lastIndexOf('.')).toLowerCase();
    if (!PEM_EXTS.includes(ext)) { reject('Unsupported file type'); return; }
    const r = new FileReader();
    r.onload = () => resolve(r.result as string);
    r.onerror = () => reject('Read error');
    r.readAsText(file);
  });
}

export default function TLSPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const queryClient = useQueryClient();

  const { data: certs = [] } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts });

  const [modalOpen, setModalOpen] = useState(false);
  const [certTab, setCertTab] = useState('paste');
  const [clickPos, setClickPos] = useState<{ x: number; y: number } | undefined>();
  const [saving, setSaving] = useState(false);
  const [generating, setGenerating] = useState(false);

  // Common fields
  const [certId, setCertId] = useState('');
  const [certName, setCertName] = useState('');
  const [editMode, setEditMode] = useState(false);

  // CA signing
  const [caId, setCaId] = useState('');
  const [cn, setCn] = useState('');

  // Manual input (paste)
  const [certPem, setCertPem] = useState('');
  const [keyPem, setKeyPem] = useState('');

  // Drag-drop PEM file handler
  const makeDrop = useCallback((setter: (v: string) => void) => ({
    onDragOver: (e: DragEvent) => { e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; },
    onDrop: (e: DragEvent) => {
      e.preventDefault();
      const file = e.dataTransfer.files[0];
      if (file) readPemFile(file).then(setter).catch((err) => toast.error(String(err)));
    },
  }), [toast]);

  // File path
  const [certPath, setCertPath] = useState('');
  const [keyPath, setKeyPath] = useState('');

  const openNew = (e: MouseEvent) => {
    setEditMode(false);
    setClickPos({ x: e.clientX, y: e.clientY });
    setCertId(''); setCertName(''); setCertPem(''); setKeyPem('');
    setCertPath(''); setKeyPath(''); setCaId(''); setCn('');
    setCertTab('paste');
    setModalOpen(true);
  };

  const handleEdit = async (cert: CertInfo, e: MouseEvent) => {
    setClickPos({ x: e.clientX, y: e.clientY });
    setCertId(cert.id);
    setCertName(cert.name || '');
    setCertPem(''); setKeyPem('');
    setCertPath(''); setKeyPath(''); setCaId(''); setCn('');
    setCertTab('paste');
    setEditMode(true);
    try {
      const pem = await api.getCertPem(cert.id);
      if (pem.cert) setCertPem(pem.cert);
      if (pem.key) setKeyPem(pem.key);
    } catch { /* ignore */ }
    setModalOpen(true);
  };

  const handleGenerate = async () => {
    if (!certId) { toast.error(t('tls.fillIdFirst')); return; }
    setGenerating(true);
    try {
      if (caId) {
        await api.signCert({ ca_id: caId, id: certId, name: certName, cn: cn || certId, days: 7300 });
      } else {
        await api.generateCert({ id: certId, name: certName, domains: [certId], days: 3650 });
      }
      // Fetch generated PEM to fill textareas — don't refresh cert list yet
      const pem = await api.getCertPem(certId);
      setCertPem(pem.cert || '');
      setKeyPem(pem.key || '');
      setCertTab('paste');
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setGenerating(false); }
  };

  const handleSave = async () => {
    if (!certId) { toast.error(t('tls.idRequired')); return; }
    setSaving(true);
    try {
      if (certTab === 'paste') {
        if (!certPem) { toast.error(t('tls.certPemRequired')); setSaving(false); return; }
        await api.importCert({ id: certId, name: certName, cert: certPem, key: keyPem || undefined });
      } else {
        if (!certPath) { toast.error(t('tls.certPathRequired')); setSaving(false); return; }
        await api.importCertPath({ id: certId, name: certName, cert_path: certPath, key_path: keyPath || undefined });
      }
      toast.success(t('tls.certSaved'));
      queryClient.invalidateQueries({ queryKey: ['certs'] });
      setModalOpen(false);
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setSaving(false); }
  };

  const handleDelete = async (cert: CertInfo) => {
    const ok = await confirm({
      title: t('tls.deleteTitle'),
      message: t('tls.deleteConfirm', { id: cert.id }),
      danger: true, confirmText: t('app.delete'), cancelText: t('app.cancel'),
    });
    if (!ok) return;
    try {
      await api.deleteCert(cert.id);
      toast.success(t('tls.deleted', { id: cert.id }));
      queryClient.invalidateQueries({ queryKey: ['certs'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  };

  const isExpired = (date: string) => {
    try { return new Date(date) < new Date(); } catch { return false; }
  };

  const caOptions = [
    { value: '', label: t('tls.noneSelfSigned') },
    ...certs.filter((c) => c.is_ca && !!c.key_file).map((c) => ({ value: c.id, label: c.name || c.id })),
  ];

  const selection = useSelection(certs.map((c) => c.id));

  const bulkDelete = useCallback(async () => {
    const ok = await confirm({
      title: t('app.bulkDelete'), message: t('tls.deleteConfirm', { id: `${selection.count} certs` }),
      danger: true, confirmText: t('app.delete'), cancelText: t('app.cancel'),
    });
    if (!ok) return;
    try {
      await Promise.all([...selection.selected].map((id) => api.deleteCert(id)));
      toast.success(`${t('app.bulkDelete')}: ${selection.count}`);
      queryClient.invalidateQueries({ queryKey: ['certs'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [selection, confirm, queryClient, toast, t]);

  const certColumns: Column<CertInfo>[] = [
    {
      key: 'name', title: t('tls.name'), render: (cert) => (
        <>
          <b>{cert.name || cert.id}</b>
          <span className="peer-addr-sub">{cert.id}</span>
          {isExpired(cert.not_after) && <> <Badge variant="muted">{t('tls.expired')}</Badge></>}
        </>
      ),
    },
    { key: 'subject', title: t('tls.subject'), render: (cert) => cert.subject },
    { key: 'issuer', title: t('tls.issuer'), render: (cert) => <>{cert.issuer}{cert.is_ca && <> <Badge variant="blue">CA</Badge></>}</> },
    { key: 'expires', title: t('tls.expires'), render: (cert) => <span className="mono">{cert.not_after}</span> },
    { key: 'key', title: t('tls.hasKey'), render: (cert) => cert.key_file ? <Badge variant="green">{t('app.yes')}</Badge> : <Badge variant="muted">{t('app.no')}</Badge> },
    {
      key: 'actions', title: '', width: '40px', render: (cert) => (
        <button className="hy-row-edit" onClick={(e) => handleEdit(cert, e)} title={t('app.edit')}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7"/><path d="M18.5 2.5a2.12 2.12 0 013 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
        </button>
      ),
    },
  ];

  return (
    <div className="hy-page">
      <Card
        fill={1}
        title={t('tls.title')}
        count={certs.length}
        actions={
          <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <BulkActionBar count={selection.count} onClear={selection.clear}>
              <Button size="sm" variant="danger" onClick={bulkDelete}>{t('app.bulkDelete')}</Button>
            </BulkActionBar>
            <Button size="sm" variant="primary" onClick={openNew}>{t('tls.new')}</Button>
          </div>
        }
        noPadding
      >
        <Table
          columns={certColumns}
          data={certs}
          rowKey={(c) => c.id}
          rowClassName={(c) => isExpired(c.not_after) ? 'disabled-row' : undefined}
          emptyText={t('tls.noCerts')}
          selection={selection}
        />
      </Card>

      {/* Cert Modal — matches old frontend: 2 tabs + generate button + CA select */}
      <Modal
        open={modalOpen} onClose={() => setModalOpen(false)}
        title={editMode ? t('tls.editTitle') : t('tls.newTitle')}
        animateFrom={clickPos}
        footer={
          <>
            <Button onClick={() => setModalOpen(false)}>{t('app.cancel')}</Button>
            <Button variant="primary" onClick={handleSave} loading={saving}>
              {editMode ? t('app.save') : t('tls.new')}
            </Button>
          </>
        }
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <FormGrid>
            <FormGroup label={t('tls.id')} required>
              <Input value={certId} onChange={(e) => setCertId(e.target.value)} placeholder="e.g. my-cert" disabled={editMode} />
            </FormGroup>
            <FormGroup label={t('tls.name')}>
              <Input value={certName} onChange={(e) => setCertName(e.target.value)} placeholder={t('tls.optional')} />
            </FormGroup>
          </FormGrid>

          {/* CA signing select */}
          {!editMode && (
            <FormGroup label={t('tls.signWithCA')}>
              <Select value={caId} onChange={(e) => setCaId(e.target.value)} options={caOptions} />
            </FormGroup>
          )}

          {/* CN field — visible when CA selected */}
          {caId && !editMode && (
            <FormGroup label={t('tls.commonName')} required>
              <Input value={cn} onChange={(e) => setCn(e.target.value)} placeholder="e.g. vpn.example.com" />
            </FormGroup>
          )}

          {/* Tabs + Generate button */}
          {(!caId || editMode) && (
            <>
              <Tabs
                items={[
                  { key: 'paste', label: t('tls.manualInput') },
                  { key: 'path', label: t('tls.filePath') },
                ]}
                activeKey={certTab}
                onChange={setCertTab}
                addon={
                  <button
                    className="hy-circle-btn"
                    title={t('tls.generate')}
                    onClick={handleGenerate}
                    disabled={generating}
                  >
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                      <path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4"/>
                    </svg>
                  </button>
                }
              />

              <TabPanel activeKey={certTab} keys={['paste', 'path']}>
                {certTab === 'paste' ? (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
                    <FormGroup label={t('tls.certPem')} required>
                      <Textarea value={certPem} onChange={(e) => setCertPem(e.target.value)} rows={5} monospace placeholder="-----BEGIN CERTIFICATE-----" {...makeDrop(setCertPem)} />
                    </FormGroup>
                    <FormGroup label={t('tls.keyPem')}>
                      <Textarea value={keyPem} onChange={(e) => setKeyPem(e.target.value)} rows={4} monospace placeholder="-----BEGIN EC PRIVATE KEY-----" {...makeDrop(setKeyPem)} />
                    </FormGroup>
                  </div>
                ) : (
                  <FormGrid>
                    <FormGroup label={t('tls.certPath')} required>
                      <Input value={certPath} onChange={(e) => setCertPath(e.target.value)} placeholder="/etc/ssl/cert.pem" />
                    </FormGroup>
                    <FormGroup label={t('tls.keyPath')}>
                      <Input value={keyPath} onChange={(e) => setKeyPath(e.target.value)} placeholder="/etc/ssl/key.pem" />
                    </FormGroup>
                  </FormGrid>
                )}
              </TabPanel>
            </>
          )}
        </div>
      </Modal>
    </div>
  );
}
