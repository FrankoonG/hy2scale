import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { Modal, Button, Input, FormGroup, FormStack, IconButton, IconWandSparkles, useToast } from '@hy2scale/ui';
import { ExitPathList, exitPathToApi, apiToExitPath, type ExitPathValue } from '@/components/ExitPathList';
import { useDialogInit } from '@/hooks/useFormInit';
import * as api from '@/api';
import type { WireGuardPeer } from '@/api';

interface Props {
  open: boolean;
  onClose: () => void;
  animateFrom?: { x: number; y: number };
  /** null = new peer. */
  editing: WireGuardPeer | null;
  /** Suggested allowed IPs for a new peer. */
  suggestIPs: () => string;
}

/** Add / edit one WireGuard peer. A new peer gets a fresh key pair. */
export default function WgPeerModal({ open, onClose, animateFrom, editing, suggestIPs }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();

  const [name, setName] = useState('');
  const [pubKey, setPubKey] = useState('');
  const [privKey, setPrivKey] = useState('');
  const [allowedIPs, setAllowedIPs] = useState('');
  const [keepalive, setKeepalive] = useState('');
  const [exitPath, setExitPath] = useState<ExitPathValue>({ paths: [''], mode: '' });
  const [saving, setSaving] = useState(false);

  const generateKey = async (quiet = false) => {
    try {
      const { private_key, public_key } = await api.generateWGKey();
      setPrivKey(private_key);
      setPubKey(public_key);
    } catch (e: any) { if (!quiet) toast.error(String(e.message || e)); }
  };

  useDialogInit(open, editing?.name ?? null, () => {
    if (editing) {
      setName(editing.name); setPubKey(editing.public_key); setPrivKey(editing.private_key);
      setAllowedIPs(editing.allowed_ips); setKeepalive(String(editing.keepalive || ''));
      setExitPath(apiToExitPath(editing.exit_via, editing.exit_paths, editing.exit_mode));
    } else {
      setName(''); setPrivKey(''); setPubKey('');
      setKeepalive('25');
      setExitPath({ paths: [''], mode: '' });
      setAllowedIPs(suggestIPs());
      generateKey(true);
    }
  });

  const handleSave = async () => {
    if (!name) { toast.error(t('wg.nameRequired')); return; }
    if (!pubKey && !privKey) { toast.error(t('wg.pubKeyRequired')); return; }
    if (!allowedIPs) { toast.error(t('wg.allowedIpsRequired')); return; }

    setSaving(true);
    const data: WireGuardPeer = {
      name, public_key: pubKey, private_key: privKey,
      allowed_ips: allowedIPs, keepalive: parseInt(keepalive) || 0, ...exitPathToApi(exitPath),
    } as any;
    try {
      if (editing) {
        await api.updateWGPeer(editing.name, data);
        toast.success(t('wg.peerUpdated'));
      } else {
        await api.createWGPeer(data);
        toast.success(t('wg.peerAdded'));
      }
      queryClient.invalidateQueries({ queryKey: ['wireguard'] });
      onClose();
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setSaving(false); }
  };

  return (
    <Modal
      open={open} onClose={onClose}
      title={editing ? t('wg.editPeerPrefix', { name: editing.name }) : t('wg.addPeerTitle')}
      animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          <Button variant="primary" onClick={handleSave} loading={saving}>
            {editing ? t('app.save') : t('app.add')}
          </Button>
        </>
      }
    >
      <FormStack>
        <FormGroup label={t('wg.peerName')} required>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="phone" />
        </FormGroup>
        <FormGroup label={t('wg.peerPubKey')} required>
          <div className="input-with-action">
            <Input className="mono small" value={pubKey} onChange={(e) => setPubKey(e.target.value)} />
            <IconButton icon={IconWandSparkles} label={t('wg.generateKey')} variant="circle" onClick={() => generateKey()} />
          </div>
        </FormGroup>
        <FormGroup label={t('wg.peerPrivKey')}>
          <Input className="mono small" value={privKey} onChange={(e) => setPrivKey(e.target.value)} placeholder={t('wg.privKeyHint')} />
        </FormGroup>
        <FormGroup label={t('wg.peerAllowedIPs')} required>
          <Input value={allowedIPs} onChange={(e) => setAllowedIPs(e.target.value)} placeholder="10.0.0.2/32, fd00:9::2/128" />
        </FormGroup>
        <ExitPathList value={exitPath} onChange={setExitPath} label={t('wg.peerExitVia')} />
        <FormGroup label={t('wg.peerKeepalive')}>
          <Input type="number" value={keepalive} onChange={(e) => setKeepalive(e.target.value)} placeholder="25" />
        </FormGroup>
      </FormStack>
    </Modal>
  );
}
