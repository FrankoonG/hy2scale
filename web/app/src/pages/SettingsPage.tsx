import { useState, useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Card, Button, Input, PasswordInput, Toggle, Select, FormGroup, FormGrid, Tabs, TabPanel, Badge, useToast, useConfirm,
} from '@hy2scale/ui';
import * as api from '@/api';
import { useAuthStore } from '@/store/auth';
import { sha256 } from '@/hooks/useAuth';

export default function SettingsPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const logout = useAuthStore((s) => s.logout);

  const { data: settings } = useQuery({ queryKey: ['settings'], queryFn: api.getUISettings });
  const { data: certs = [] } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts });
  const { data: archInfo } = useQuery({ queryKey: ['systemArch'], queryFn: api.getSystemArch });

  const [activeTab, setActiveTab] = useState<'system' | 'web' | 'upgrade'>('system');

  // UI Settings
  const [listen, setListen] = useState('');
  const [basePath, setBasePath] = useState('');
  const [forceHttps, setForceHttps] = useState(false);
  const [httpsCertId, setHttpsCertId] = useState('');
  const [sessionTimeout, setSessionTimeout] = useState('12');
  const [dns, setDns] = useState('');
  const [savingUI, setSavingUI] = useState(false);
  const [savingDns, setSavingDns] = useState(false);

  // Password
  const [curPw, setCurPw] = useState('');
  const [newUser, setNewUser] = useState('');
  const [newPw, setNewPw] = useState('');
  const [confirmPw, setConfirmPw] = useState('');
  const [savingPw, setSavingPw] = useState(false);

  // Restore & Upgrade
  const restoreRef = useRef<HTMLInputElement>(null);
  const upgradeRef = useRef<HTMLInputElement>(null);
  const [upgrading, setUpgrading] = useState(false);

  useEffect(() => {
    if (settings) {
      setListen(settings.listen || '');
      setBasePath(settings.base_path || '');
      setForceHttps(settings.force_https || false);
      setHttpsCertId(settings.https_cert_id || '');
      setSessionTimeout(String(settings.session_timeout_h || 12));
      setDns(settings.dns || '');
    }
  }, [settings]);

  const handleSaveWeb = async () => {
    setSavingUI(true);
    try {
      await api.updateUISettings({
        listen, base_path: basePath, force_https: forceHttps,
        https_cert_id: httpsCertId, session_timeout_h: parseInt(sessionTimeout) || 12,
        dns,
      });
      toast.success(t('settings.saved'));
      queryClient.invalidateQueries({ queryKey: ['settings'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setSavingUI(false); }
  };

  const handleSaveDns = async () => {
    setSavingDns(true);
    try {
      await api.updateUISettings({
        listen, base_path: basePath, force_https: forceHttps,
        https_cert_id: httpsCertId, session_timeout_h: parseInt(sessionTimeout) || 12,
        dns,
      });
      toast.success(t('settings.saved'));
      queryClient.invalidateQueries({ queryKey: ['settings'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setSavingDns(false); }
  };

  const handleChangePw = async () => {
    if (!curPw) { toast.error(t('settings.passwordRequired')); return; }
    if (!newUser && !newPw) { toast.error(t('settings.enterNewCreds')); return; }
    if (newPw && newPw !== confirmPw) { toast.error(t('error.passwordMismatch')); return; }

    setSavingPw(true);
    try {
      const curHash = await sha256(curPw);
      const newHash = newPw ? await sha256(newPw) : undefined;
      await api.changePassword({
        current_password: curHash,
        new_username: newUser || undefined,
        new_password: newHash,
      });
      toast.success(t('settings.passwordUpdated'));
      setTimeout(() => { logout(); navigate('/login'); }, 2000);
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setSavingPw(false); }
  };

  const handleBackup = async () => {
    try {
      const blob = await api.downloadBackup();
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url; a.download = 'hy2scale-backup.tar'; a.click();
      URL.revokeObjectURL(url);
    } catch (e: any) { toast.error(String(e.message || e)); }
  };

  const handleRestore = async (file: File) => {
    const ok = await confirm({
      title: t('settings.backup'),
      message: t('settings.restoreConfirm'),
      danger: true, confirmText: t('app.confirm'), cancelText: t('app.cancel'),
    });
    if (!ok) return;
    try {
      toast.info(t('settings.restoreUploading'));
      await api.uploadRestore(file);
      toast.success(t('settings.restoreComplete'));
      setTimeout(() => window.location.reload(), 3000);
    } catch (e: any) { toast.error(t('settings.restoreFailed') + ': ' + String(e.message || e)); }
  };

  const handleUpgrade = async (file: File) => {
    if (!file.name.endsWith('.tar.gz') && !file.name.endsWith('.tgz')) {
      toast.error(t('settings.upgradeFailed') + ': expected .tar.gz');
      return;
    }
    const ok = await confirm({
      title: t('settings.upgradeTitle'),
      message: t('settings.upgradeConfirm'),
      danger: true, confirmText: t('app.confirm'), cancelText: t('app.cancel'),
    });
    if (!ok) return;
    setUpgrading(true);
    try {
      toast.info(t('settings.upgradeUploading'));
      await api.uploadUpgrade(file);
      toast.success(t('settings.upgradeComplete'));
      setTimeout(() => window.location.reload(), 5000);
    } catch (e: any) {
      toast.error(t('settings.upgradeFailed') + ': ' + String(e.message || e));
    } finally { setUpgrading(false); }
  };

  const certOptions = [
    { value: '', label: '\u2014' },
    ...certs.map((c) => ({ value: c.id, label: c.name || c.id })),
  ];

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
      <Tabs
        items={[
          { key: 'system', label: t('settings.system') },
          { key: 'web', label: t('settings.web') },
          { key: 'upgrade', label: t('settings.upgrade') },
        ]}
        activeKey={activeTab}
        onChange={(key) => setActiveTab(key as 'system' | 'web' | 'upgrade')}
      />

      <TabPanel activeKey={activeTab} keys={['system', 'web', 'upgrade']}>
        {activeTab === 'system' ? (
          <>
            {/* System: DNS */}
            <Card title={t('settings.system')}>
              <div style={{ maxWidth: 400, display: 'flex', flexDirection: 'column', gap: 14 }}>
                <FormGroup label={t('settings.dns')}>
                  <Input value={dns} onChange={(e) => setDns(e.target.value)} placeholder="8.8.8.8,1.1.1.1" />
                </FormGroup>
                <Button variant="primary" onClick={handleSaveDns} loading={savingDns} style={{ alignSelf: 'flex-start' }}>{t('app.save')}</Button>
              </div>
            </Card>

            {/* Credentials */}
            <Card title={t('settings.changePassword')}>
              <div style={{ maxWidth: 400, display: 'flex', flexDirection: 'column', gap: 14 }}>
                <FormGroup label={t('settings.currentPassword')} required>
                  <PasswordInput value={curPw} onChange={(e) => setCurPw(e.target.value)} />
                </FormGroup>
                <FormGroup label={t('settings.newUsernameOpt')}>
                  <Input value={newUser} onChange={(e) => setNewUser(e.target.value)} />
                </FormGroup>
                <FormGroup label={t('settings.newPassword')}>
                  <PasswordInput value={newPw} onChange={(e) => setNewPw(e.target.value)} onGenerate={(pw) => { setNewPw(pw); setConfirmPw(pw); }} />
                </FormGroup>
                <FormGroup label={t('settings.confirmPassword')}>
                  <PasswordInput value={confirmPw} onChange={(e) => setConfirmPw(e.target.value)} />
                </FormGroup>
                <Button variant="primary" onClick={handleChangePw} loading={savingPw} style={{ alignSelf: 'flex-start' }}>{t('settings.update')}</Button>
              </div>
            </Card>
          </>
        ) : activeTab === 'web' ? (
          <Card title={t('settings.webUi')}>
            <div style={{ maxWidth: 400, display: 'flex', flexDirection: 'column', gap: 14 }}>
              <FormGroup label={t('settings.listenAddress')}>
                <Input value={listen} onChange={(e) => setListen(e.target.value)} placeholder=":5565" />
              </FormGroup>
              <FormGroup label={t('settings.basePath')}>
                <Input value={basePath} onChange={(e) => setBasePath(e.target.value)} placeholder="/scale" />
              </FormGroup>
              <FormGroup label={t('settings.forceHttps')}>
                <Toggle checked={forceHttps} onChange={(e) => setForceHttps(e.target.checked)} />
              </FormGroup>
              <FormGroup label={t('settings.httpsCert')}>
                <Select value={httpsCertId} onChange={(e) => setHttpsCertId(e.target.value)} options={certOptions} />
              </FormGroup>
              <FormGroup label={t('settings.sessionTimeout')}>
                <Input type="number" value={sessionTimeout} onChange={(e) => setSessionTimeout(e.target.value)} suffix="h" />
              </FormGroup>
              <div style={{ fontSize: 11, color: 'var(--text-muted)' }}>{t('settings.restartRequired')}</div>
              <Button variant="primary" onClick={handleSaveWeb} loading={savingUI} style={{ alignSelf: 'flex-start' }}>{t('app.save')}</Button>
            </div>
          </Card>
        ) : (
          <>
            {/* Upgrade Binary — only in Docker */}
            <Card title={t('settings.upgradeTitle')}>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
                {archInfo && (
                  <div style={{ display: 'flex', gap: 12, alignItems: 'center', flexWrap: 'wrap' }}>
                    <span style={{ fontSize: 13, color: 'var(--text-secondary)' }}>{t('settings.currentArch')}:</span>
                    <Badge>{archInfo.os}/{archInfo.arch}</Badge>
                    <span style={{ fontSize: 13, color: 'var(--text-secondary)' }}>{t('settings.currentVersion')}:</span>
                    <Badge>{archInfo.version}</Badge>
                  </div>
                )}
                {archInfo?.in_docker ? (
                  <>
                    <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('settings.upgradeDesc')}</div>
                    <div>
                      <Button variant="primary" onClick={() => upgradeRef.current?.click()} loading={upgrading}>
                        {t('settings.uploadPackage')}
                      </Button>
                      <input
                        ref={upgradeRef}
                        type="file"
                        accept=".tar.gz,.tgz"
                        style={{ display: 'none' }}
                        onChange={(e) => {
                          const file = e.target.files?.[0];
                          if (file) handleUpgrade(file);
                          e.target.value = '';
                        }}
                      />
                    </div>
                  </>
                ) : (
                  <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('settings.upgradeNotAvailable')}</div>
                )}
              </div>
            </Card>

            {/* Backup & Restore */}
            <Card title={t('settings.backup')}>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
                <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('settings.backupDesc')}</div>
                <div style={{ display: 'flex', gap: 12 }}>
                  <Button onClick={handleBackup}>{t('settings.downloadBackup')}</Button>
                  <Button variant="danger" onClick={() => restoreRef.current?.click()}>{t('settings.restoreFromFile')}</Button>
                  <input
                    ref={restoreRef}
                    type="file"
                    accept=".tar"
                    style={{ display: 'none' }}
                    onChange={(e) => {
                      const file = e.target.files?.[0];
                      if (file) handleRestore(file);
                      e.target.value = '';
                    }}
                  />
                </div>
              </div>
            </Card>
          </>
        )}
      </TabPanel>
    </div>
  );
}
