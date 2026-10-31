import { useCallback, useState, type DragEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Modal, Button, Input, Textarea, Select, Tabs, TabPanel, FormGroup, FormGrid, FormStack, IconButton,
  IconWandSparkles, useToast,
} from '@hy2scale/ui';
import { useDialogInit } from '@/hooks/useFormInit';
import type { CertInfo } from '@/api';
import * as api from '@/api';

const PEM_EXTS = ['.pem', '.crt', '.cer', '.key', '.pub', '.txt'];
const MAX_FILE = 64 * 1024;

function readPemFile(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    if (file.size > MAX_FILE) { reject('tls.fileTooLarge'); return; }
    const ext = file.name.substring(file.name.lastIndexOf('.')).toLowerCase();
    if (!PEM_EXTS.includes(ext)) { reject('tls.fileUnsupported'); return; }
    const r = new FileReader();
    r.onload = () => resolve(r.result as string);
    r.onerror = () => reject('tls.fileReadError');
    r.readAsText(file);
  });
}

interface Props {
  open: boolean;
  onClose: () => void;
  animateFrom?: { x: number; y: number };
  /** null = new certificate. */
  editing: CertInfo | null;
}

/** New / edit certificate: paste PEM or file paths, generate, or sign with a CA. */
export default function CertModal({ open, onClose, animateFrom, editing }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();
  const { data: certs = [] } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts });

  const [certTab, setCertTab] = useState('paste');
  const [saving, setSaving] = useState(false);
  const [generating, setGenerating] = useState(false);
  const [certId, setCertId] = useState('');
  const [certName, setCertName] = useState('');
  const [caId, setCaId] = useState('');
  const [cn, setCn] = useState('');
  const [certPem, setCertPem] = useState('');
  const [keyPem, setKeyPem] = useState('');
  const [certPath, setCertPath] = useState('');
  const [keyPath, setKeyPath] = useState('');
  const editMode = !!editing;

  useDialogInit(open, editing?.id ?? null, () => {
    setCertId(editing?.id ?? ''); setCertName(editing?.name || '');
    setCertPem(''); setKeyPem('');
    setCertPath(''); setKeyPath(''); setCaId(''); setCn('');
    setCertTab('paste');
    if (editing) {
      api.getCertPem(editing.id).then((pem) => {
        if (pem.cert) setCertPem(pem.cert);
        if (pem.key) setKeyPem(pem.key);
      }).catch(() => { /* ignore */ });
    }
  });

  // Drag-drop PEM file handler
  const makeDrop = useCallback((setter: (v: string) => void) => ({
    onDragOver: (e: DragEvent) => { e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; },
    onDrop: (e: DragEvent) => {
      e.preventDefault();
      const file = e.dataTransfer.files[0];
      if (file) readPemFile(file).then(setter).catch((key) => toast.error(t(String(key))));
    },
  }), [toast, t]);

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
        if (!certPem) { toast.error(t('tls.certPemRequired')); return; }
        await api.importCert({ id: certId, name: certName, cert: certPem, key: keyPem || undefined });
      } else {
        if (!certPath) { toast.error(t('tls.certPathRequired')); return; }
        await api.importCertPath({ id: certId, name: certName, cert_path: certPath, key_path: keyPath || undefined });
      }
      toast.success(t('tls.certSaved'));
      queryClient.invalidateQueries({ queryKey: ['certs'] });
      onClose();
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setSaving(false); }
  };

  const caOptions = [
    { value: '', label: t('tls.noneSelfSigned') },
    ...certs.filter((c) => c.is_ca && !!c.key_file).map((c) => ({ value: c.id, label: c.name || c.id })),
  ];

  return (
    <Modal
      open={open} onClose={onClose}
      title={editMode ? t('tls.editTitle') : t('tls.newTitle')}
      animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          <Button variant="primary" onClick={handleSave} loading={saving}>
            {editMode ? t('app.save') : t('tls.new')}
          </Button>
        </>
      }
    >
      <FormStack>
        <FormGrid>
          <FormGroup label={t('tls.id')} required>
            <Input value={certId} onChange={(e) => setCertId(e.target.value)} placeholder="my-cert" disabled={editMode} />
          </FormGroup>
          <FormGroup label={t('tls.name')}>
            <Input value={certName} onChange={(e) => setCertName(e.target.value)} placeholder={t('tls.optional')} />
          </FormGroup>
        </FormGrid>

        {!editMode && (
          <FormGroup label={t('tls.signWithCA')}>
            <Select value={caId} onChange={(e) => setCaId(e.target.value)} options={caOptions} />
          </FormGroup>
        )}

        {/* Common name — when signing with a CA */}
        {caId && !editMode && (
          <FormGroup label={t('tls.commonName')} required>
            <Input value={cn} onChange={(e) => setCn(e.target.value)} placeholder="vpn.example.com" />
          </FormGroup>
        )}

        {(!caId || editMode) && (
          <>
            <Tabs
              items={[
                { key: 'paste', label: t('tls.manualInput') },
                { key: 'path', label: t('tls.filePath') },
              ]}
              activeKey={certTab}
              onChange={setCertTab}
              addon={<IconButton icon={IconWandSparkles} label={t('tls.generate')} variant="circle" onClick={handleGenerate} disabled={generating} />}
            />

            <TabPanel activeKey={certTab} keys={['paste', 'path']}>
              {certTab === 'paste' ? (
                <FormStack>
                  <FormGroup label={t('tls.certPem')} required>
                    <Textarea value={certPem} onChange={(e) => setCertPem(e.target.value)} rows={5} monospace placeholder="-----BEGIN CERTIFICATE-----" {...makeDrop(setCertPem)} />
                  </FormGroup>
                  <FormGroup label={t('tls.keyPem')}>
                    <Textarea value={keyPem} onChange={(e) => setKeyPem(e.target.value)} rows={4} monospace placeholder="-----BEGIN EC PRIVATE KEY-----" {...makeDrop(setKeyPem)} />
                  </FormGroup>
                </FormStack>
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
      </FormStack>
    </Modal>
  );
}
