import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { Modal, Button, Input, PasswordInput, FormGroup, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import { useNodeStore } from '@/store/node';

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
  const [listen, setListen] = useState('');
  const [password, setPassword] = useState('');

  useEffect(() => {
    if (open && node?.server) {
      setListen(node.server.listen || '');
      setPassword(node.server.password || '');
    }
  }, [open, node]);

  const handleSave = async () => {
    setLoading(true);
    try {
      await api.updateNode({
        server: listen ? { listen, password, tls_cert: '', tls_key: '' } : null,
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

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('nodes.editSelf')}
      animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          <Button variant="primary" onClick={handleSave} loading={loading}>{t('app.save')}</Button>
        </>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <FormGroup label={`${t('nodes.hy2Server')} — ${t('nodes.listenUdp')}`}>
          <Input
            value={listen}
            onChange={(e) => setListen(e.target.value)}
            placeholder=":5565"
          />
        </FormGroup>
        <FormGroup label={t('nodes.password')}>
          <PasswordInput
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </FormGroup>
        <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>
          {!listen && t('nodes.noHy2Server')}
          {listen && `${t('nodes.selfSignedAuto')}`}
        </div>
      </div>
    </Modal>
  );
}
