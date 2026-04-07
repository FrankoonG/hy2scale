import { useState, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Card, Button, Badge, Tabs, Modal, Input, Textarea, Select,
  FormGroup, FormGrid, TabPanel, useToast, useConfirm,
} from '@hy2scale/ui';
import * as api from '@/api';
import type { CertInfo } from '@/api';

export default function TLSPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const queryClient = useQueryClient();

  const { data: certs = [] } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts });

  const [modalOpen, setModalOpen] = useState(false);
  const [certTab, setCertTab] = useState('manual');
  const [clickPos, setClickPos] = useState<{ x: number; y: number } | undefined>();
  const [saving, setSaving] = useState(false);

  // Manual input
  const [certId, setCertId] = useState('');
  const [certName, setCertName] = useState('');
  const [certPem, setCertPem] = useState('');
  const [keyPem, setKeyPem] = useState('');

  // File path
  const [certPath, setCertPath] = useState('');
  const [keyPath, setKeyPath] = useState('');

  // Generate
  const [domains, setDomains] = useState('');

  // Sign
  const [caId, setCaId] = useState('');
  const [cn, setCn] = useState('');
  const [editMode, setEditMode] = useState(false);

  const openNew = (e: MouseEvent) => {
    setEditMode(false);
    setClickPos({ x: e.clientX, y: e.clientY });
    setCertId(''); setCertName(''); setCertPem(''); setKeyPem('');
    setCertPath(''); setKeyPath(''); setDomains(''); setCaId(''); setCn('');
    setCertTab('manual');
    setModalOpen(true);
  };

  const handleEdit = async (cert: CertInfo, e: MouseEvent) => {
    setClickPos({ x: e.clientX, y: e.clientY });
    setCertId(cert.id);
    setCertName(cert.name || '');
    setCertPem(''); setKeyPem('');
    setCertPath(''); setKeyPath(''); setDomains(''); setCaId(''); setCn('');
    setCertTab('manual');
    setEditMode(true);
    try {
      const pem = await api.getCertPem(cert.id);
      if (pem.cert) setCertPem(pem.cert);
      if (pem.key) setKeyPem(pem.key);
    } catch { /* ignore */ }
    setModalOpen(true);
  };

  const handleSave = async () => {
    if (!certId) { toast.error(t('tls.idRequired')); return; }
    setSaving(true);
    try {
      switch (certTab) {
        case 'manual':
          if (!certPem) { toast.error(t('tls.certPemRequired')); setSaving(false); return; }
          await api.importCert({ id: certId, name: certName, cert: certPem, key: keyPem || undefined });
          break;
        case 'path':
          if (!certPath) { toast.error(t('tls.certPathRequired')); setSaving(false); return; }
          await api.importCertPath({ id: certId, name: certName, cert_path: certPath, key_path: keyPath || undefined });
          break;
        case 'generate':
          await api.generateCert({
            id: certId, name: certName,
            domains: domains.split(/[,\n]/).map((d) => d.trim()).filter(Boolean),
          });
          break;
        case 'sign':
          if (!caId) { toast.error(t('tls.fillIdFirst')); setSaving(false); return; }
          await api.signCert({ ca_id: caId, id: certId, name: certName, cn });
          break;
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
    ...certs.filter((c) => c.is_ca).map((c) => ({ value: c.id, label: c.name || c.id })),
  ];

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
      <Card
        title={t('tls.title')}
        count={certs.length}
        actions={<Button size="sm" variant="primary" onClick={openNew}>{t('tls.new')}</Button>}
      >
        {certs.length === 0 ? (
          <div className="hy-empty" dangerouslySetInnerHTML={{ __html: t('tls.noCerts') }} />
        ) : (
          <div className="hy-table-wrap">
            <table className="hy-table">
              <thead>
                <tr>
                  <th>{t('tls.name')}</th>
                  <th>{t('tls.subject')}</th>
                  <th>{t('tls.issuer')}</th>
                  <th>{t('tls.expires')}</th>
                  <th>{t('tls.hasKey')}</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {certs.map((cert) => (
                  <tr key={cert.id} style={isExpired(cert.expires) ? { opacity: 0.45 } : undefined}>
                    <td>
                      <b>{cert.name || cert.id}</b>
                      <span className="peer-addr-sub">{cert.id}</span>
                      {isExpired(cert.expires) && <> <Badge variant="muted">{t('tls.expired')}</Badge></>}
                    </td>
                    <td>{cert.subject}</td>
                    <td>{cert.issuer}{cert.is_ca && <> <Badge variant="blue">CA</Badge></>}</td>
                    <td><span style={{ fontFamily: 'var(--mono)', fontSize: 12 }}>{cert.expires}</span></td>
                    <td>{cert.has_key ? <Badge variant="green">{t('app.yes')}</Badge> : <Badge variant="muted">{t('app.no')}</Badge>}</td>
                    <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                      <button className="act-btn edit" onClick={(e) => handleEdit(cert, e)}>{t('app.edit')}</button>
                      {' '}
                      <button className="act-btn danger" onClick={() => handleDelete(cert)}>{t('app.delete')}</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {/* New Cert Modal */}
      <Modal
        open={modalOpen} onClose={() => setModalOpen(false)}
        title={t('tls.newTitle')}
        wide
        animateFrom={clickPos}
        footer={
          <>
            <Button onClick={() => setModalOpen(false)}>{t('app.cancel')}</Button>
            <Button variant="primary" onClick={handleSave} loading={saving}>{t('app.save')}</Button>
          </>
        }
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <FormGrid>
            <FormGroup label={t('tls.id')} required>
              <Input value={certId} onChange={(e) => setCertId(e.target.value)} />
            </FormGroup>
            <FormGroup label={t('tls.name')}>
              <Input value={certName} onChange={(e) => setCertName(e.target.value)} />
            </FormGroup>
          </FormGrid>

          <Tabs
            items={[
              { key: 'manual', label: t('tls.manualInput') },
              { key: 'path', label: t('tls.filePath') },
              { key: 'generate', label: t('tls.generate') },
              { key: 'sign', label: t('tls.signWithCA') },
            ]}
            activeKey={certTab}
            onChange={setCertTab}
          />

          <TabPanel activeKey={certTab} keys={['manual', 'path', 'generate', 'sign']}>
            {certTab === 'manual' && (
              <>
                <FormGroup label={t('tls.certPem')} required>
                  <Textarea value={certPem} onChange={(e) => setCertPem(e.target.value)} rows={5} monospace />
                </FormGroup>
                <FormGroup label={t('tls.keyPem')}>
                  <Textarea value={keyPem} onChange={(e) => setKeyPem(e.target.value)} rows={5} monospace />
                </FormGroup>
              </>
            )}
            {certTab === 'path' && (
              <FormGrid>
                <FormGroup label={t('tls.certPath')} required>
                  <Input value={certPath} onChange={(e) => setCertPath(e.target.value)} placeholder="/etc/ssl/cert.pem" />
                </FormGroup>
                <FormGroup label={t('tls.keyPath')}>
                  <Input value={keyPath} onChange={(e) => setKeyPath(e.target.value)} placeholder="/etc/ssl/key.pem" />
                </FormGroup>
              </FormGrid>
            )}
            {certTab === 'generate' && (
              <FormGroup label={`${t('tls.subject')} (domains)`}>
                <Textarea value={domains} onChange={(e) => setDomains(e.target.value)} rows={3} monospace placeholder="example.com&#10;*.example.com" />
              </FormGroup>
            )}
            {certTab === 'sign' && (
              <>
                <FormGroup label="CA">
                  <Select value={caId} onChange={(e) => setCaId(e.target.value)} options={caOptions} />
                </FormGroup>
                <FormGroup label={t('tls.commonName')}>
                  <Input value={cn} onChange={(e) => setCn(e.target.value)} />
                </FormGroup>
              </>
            )}
          </TabPanel>
        </div>
      </Modal>
    </div>
  );
}
