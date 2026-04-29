import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { Modal, Button, Input, PasswordInput, Toggle, FormGroup, FormGrid, Tabs, TabPanel, useToast } from '@hy2scale/ui';
import { ExitPathList, exitPathToApi, apiToExitPath, type ExitPathValue } from './ExitPathList';
import * as api from '@/api';
import { PROXY_REGISTRY } from '@/config/proxyRegistry';
import { useNodeStore } from '@/store/node';

interface Props {
  open: boolean;
  onClose: () => void;
  editingId: string | null;
  animateFrom?: { x: number; y: number };
}

export default function UserModal({ open, onClose, editingId, animateFrom }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();

  const [loading, setLoading] = useState(false);
  const [tab, setTab] = useState('general');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [proxyPasswords, setProxyPasswords] = useState<Record<string, string>>({});
  // proxyDisabled tracked as a Set for O(1) toggle membership tests; the
  // backend stores it as a string[]. Empty set → user authenticates on
  // every proxy.
  const [proxyDisabled, setProxyDisabled] = useState<Set<string>>(new Set());
  const [exitPath, setExitPath] = useState<ExitPathValue>({ paths: [''], mode: '' });
  const [trafficLimit, setTrafficLimit] = useState('');
  const [expiryDate, setExpiryDate] = useState('');
  const [enabled, setEnabled] = useState(true);

  useEffect(() => {
    if (!open) return;
    setTab('general');
    if (!editingId) {
      setUsername(''); setPassword(''); setProxyPasswords({});
      setProxyDisabled(new Set());
      setExitPath({ paths: [''], mode: '' });
      setTrafficLimit(''); setExpiryDate(''); setEnabled(true);
      return;
    }
    api.getUsers().then((users) => {
      const u = users.find((u) => u.id === editingId);
      if (!u) return;
      setUsername(u.username);
      setPassword(u.password);
      setProxyPasswords(u.proxy_passwords || {});
      setProxyDisabled(new Set(u.proxy_disabled || []));
      setExitPath(apiToExitPath(u.exit_via, u.exit_paths, u.exit_mode));
      setTrafficLimit(u.traffic_limit > 0 ? String(u.traffic_limit / 1073741824) : '');
      setExpiryDate(u.expiry_date || '');
      setEnabled(u.enabled);
    });
  }, [open, editingId]);

  const handleSubmit = async () => {
    if (!username || !password) {
      toast.error(t('users.usernamePassRequired'));
      return;
    }
    // Block re-using the hy2 server password as a user account password —
    // peers see the server password in plain text via the clients[]
    // block, so this would silently leak the user's credentials to every
    // other peer. Backend enforces the same rule, this is the UI hint.
    const node = useNodeStore.getState().node;
    if (node?.server?.password && node.server.password === password) {
      toast.error(t('users.passwordCollidesServer'));
      return;
    }
    setLoading(true);
    const exitData = exitPathToApi(exitPath);
    // Filter out empty proxy passwords
    const filteredProxyPw = Object.fromEntries(
      Object.entries(proxyPasswords).filter(([, v]) => v !== '')
    );
    const proxyDisabledArr = Array.from(proxyDisabled);
    const data = {
      username,
      password,
      proxy_passwords: Object.keys(filteredProxyPw).length > 0 ? filteredProxyPw : undefined,
      proxy_disabled: proxyDisabledArr.length > 0 ? proxyDisabledArr : undefined,
      ...exitData,
      traffic_limit: trafficLimit ? parseFloat(trafficLimit) * 1073741824 : 0,
      expiry_date: expiryDate || undefined,
      enabled,
    };

    try {
      if (editingId) {
        await api.updateUser(editingId, data);
        toast.success(t('users.updated', { name: username }));
      } else {
        await api.createUser(data);
        toast.success(t('users.added', { name: username }));
      }
      queryClient.invalidateQueries({ queryKey: ['users'] });
      queryClient.invalidateQueries({ queryKey: ['userConflicts'] });
      onClose();
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  const title = editingId ? t('users.editPrefix', { name: username }) : t('users.addTitle');

  const updateProxyPw = (key: string, val: string) =>
    setProxyPasswords((prev) => ({ ...prev, [key]: val }));

  const toggleProxyEnabled = (key: string, on: boolean) =>
    setProxyDisabled((prev) => {
      const next = new Set(prev);
      if (on) next.delete(key); else next.add(key);
      return next;
    });

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          <Button variant="primary" onClick={handleSubmit} loading={loading}>{t('app.save')}</Button>
        </>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <Tabs
          items={[
            { key: 'general', label: t('users.general') },
            { key: 'proxy_pw', label: t('users.proxyAuth') },
          ]}
          activeKey={tab}
          onChange={setTab}
        />

        <TabPanel activeKey={tab} keys={['general', 'proxy_pw']}>
          {tab === 'general' ? (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
              <FormGrid>
                <FormGroup label={t('users.username')} required>
                  <Input value={username} onChange={(e) => setUsername(e.target.value)} />
                </FormGroup>
                <FormGroup label={t('users.password')} required>
                  <PasswordInput value={password} onChange={(e) => setPassword(e.target.value)} onGenerate={setPassword} />
                </FormGroup>
              </FormGrid>

              <ExitPathList value={exitPath} onChange={setExitPath} />

              <FormGrid>
                <FormGroup label={t('users.trafficLimit')}>
                  <Input type="number" value={trafficLimit} onChange={(e) => setTrafficLimit(e.target.value)} placeholder="0" suffix="GB" />
                </FormGroup>
                <FormGroup label={t('users.expiryDate')}>
                  <Input type="date" value={expiryDate} onChange={(e) => setExpiryDate(e.target.value)} />
                </FormGroup>
              </FormGrid>

              <FormGroup label={t('app.enabled')}>
                <Toggle checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
              </FormGroup>
            </div>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
              <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>
                {t('users.proxyAuthHint')}
              </div>
              {PROXY_REGISTRY.map(({ key, label, authType }) => {
                const isOn = !proxyDisabled.has(key);
                const showPwField = authType === 'password';
                return (
                  <FormGroup key={key} label={label}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                      <Toggle
                        checked={isOn}
                        onChange={(e) => toggleProxyEnabled(key, e.target.checked)}
                        data-testid={`proxy-enable-${key}`}
                      />
                      {showPwField ? (
                        <div style={{ flex: 1 }}>
                          <PasswordInput
                            value={proxyPasswords[key] || ''}
                            onChange={(e) => updateProxyPw(key, e.target.value)}
                            onGenerate={(v) => updateProxyPw(key, v)}
                            placeholder={t('users.useMainPassword')}
                            disabled={!isOn}
                          />
                        </div>
                      ) : (
                        <span style={{ fontSize: 12, color: 'var(--text-muted)' }}>
                          {isOn ? t('users.proxyAuthOn') : t('users.proxyAuthOff')}
                        </span>
                      )}
                    </div>
                  </FormGroup>
                );
              })}
            </div>
          )}
        </TabPanel>
      </div>
    </Modal>
  );
}
