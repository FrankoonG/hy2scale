import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { Modal, Button, Input, Select, FormGroup, FormGrid, FormStack, useToast } from '@hy2scale/ui';
import { useDialogInit } from '@/hooks/useFormInit';
import { useNodeStore } from '@/store/node';
import * as api from '@/api';

interface Props {
  open: boolean;
  onClose: () => void;
  animateFrom?: { x: number; y: number };
}

const YEARS = [5, 10, 20];
const KEYS = ['ecdsa-p256', 'rsa-2048', 'rsa-4096'] as const;

/** New certificate authority: a self-signed CA that signs leaf certificates. */
export default function CaModal({ open, onClose, animateFrom }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();
  const nodeName = useNodeStore((s) => s.node?.name) || 'hy2scale';
  const [id, setId] = useState('');
  const [name, setName] = useState('');
  const [cn, setCn] = useState('');
  const [org, setOrg] = useState('');
  const [years, setYears] = useState('10');
  const [key, setKey] = useState<string>('ecdsa-p256');
  const [saving, setSaving] = useState(false);

  useDialogInit(open, null, () => {
    setId(''); setName(''); setCn(''); setOrg(''); setYears('10'); setKey('ecdsa-p256');
  });

  const cnFallback = `${nodeName} CA`;
  const save = async () => {
    if (!id) { toast.error(t('tls.idRequired')); return; }
    setSaving(true);
    try {
      await api.generateCA({ id, name: name || id, cn: cn.trim() || cnFallback, org: org.trim() || undefined, days: Number(years) * 365, key });
      toast.success(t('tls.caCreated'));
      queryClient.invalidateQueries({ queryKey: ['certs'] });
      onClose();
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setSaving(false); }
  };

  return (
    <Modal
      open={open} onClose={onClose} title={t('tls.newCaTitle')} animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          <Button variant="primary" onClick={save} loading={saving} data-testid="ca-create">{t('tls.createCa')}</Button>
        </>
      }
    >
      <FormStack>
        <FormGrid>
          <FormGroup label={t('tls.id')} required>
            <Input value={id} onChange={(e) => setId(e.target.value)} placeholder="my-ca" data-testid="ca-id" />
          </FormGroup>
          <FormGroup label={t('tls.name')}>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('tls.optional')} />
          </FormGroup>
        </FormGrid>
        <FormGrid>
          <FormGroup label={t('tls.commonName')}>
            <Input value={cn} onChange={(e) => setCn(e.target.value)} placeholder={cnFallback} />
          </FormGroup>
          <FormGroup label={t('tls.organization')}>
            <Input value={org} onChange={(e) => setOrg(e.target.value)} placeholder={t('tls.optional')} />
          </FormGroup>
        </FormGrid>
        <FormGrid>
          <FormGroup label={t('tls.validity')}>
            <Select value={years} onChange={(e) => setYears(e.target.value)}
              options={YEARS.map((y) => ({ value: String(y), label: t('tls.years', { count: y }) }))} />
          </FormGroup>
          <FormGroup label={t('tls.keyType')}>
            <Select value={key} onChange={(e) => setKey(e.target.value)}
              options={KEYS.map((k) => ({ value: k, label: t(`tls.key.${k}`) }))} />
          </FormGroup>
        </FormGrid>
      </FormStack>
    </Modal>
  );
}
