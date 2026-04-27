import { useState, useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Card, Button, Input, PasswordInput, Toggle, Select, FormGroup, FormGrid, Tabs, TabPanel, Badge, Modal, useToast, useConfirm,
} from '@hy2scale/ui';
import * as api from '@/api';
import { useAuthStore } from '@/store/auth';
import { sha256 } from '@/hooks/useAuth';

/**
 * LicensePanel — renders the project's umbrella licence, the two
 * hand-maintained native components (strongSwan + friends) that
 * constrain our licence choice, and the full Go module dependency
 * list pulled from debug.BuildInfo on the server. Lives inside the
 * Upgrade card so the compliance-relevant info sits alongside the
 * "what version am I running" display.
 */
function LicensePanel() {
  const { t } = useTranslation();
  const { data } = useQuery({ queryKey: ['buildInfo'], queryFn: api.getBuildInfo, staleTime: 60_000 });
  if (!data) return null;
  return (
    <div className="hy-license-panel">
      <div className="hy-license-header">
        <span className="hy-license-label">{t('settings.license')}</span>
        <code className="hy-license-value">{data.license}</code>
        {data.repository && (
          <a className="hy-license-link" href={data.repository} target="_blank" rel="noopener noreferrer">{t('settings.sourceCode')}</a>
        )}
      </div>
      <div className="hy-license-hint">{t('settings.licenseHint')}</div>
      {data.natives && data.natives.length > 0 && (
        <details open>
          <summary className="hy-license-summary">{t('settings.nativeComponents')} · {data.natives.length}</summary>
          <div className="hy-license-table">
            {data.natives.map((n) => (
              <div key={n.name} className="hy-license-row">
                <span className="hy-license-name">{n.source ? <a href={n.source} target="_blank" rel="noopener noreferrer">{n.name}</a> : n.name}</span>
                <span className="hy-license-ver">{n.version}</span>
                <span className="hy-license-lic">{n.license}</span>
              </div>
            ))}
          </div>
        </details>
      )}
      {data.go_deps && data.go_deps.length > 0 && (
        <details>
          <summary className="hy-license-summary">{t('settings.goDependencies')} · {data.go_deps.length}</summary>
          <div className="hy-license-table hy-license-table--compact">
            {data.go_deps.map((d) => (
              <div key={d.path} className="hy-license-row">
                <span className="hy-license-name">{d.path}</span>
                <span className="hy-license-ver">{d.version}</span>
              </div>
            ))}
          </div>
        </details>
      )}
    </div>
  );
}

export default function SettingsPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const logout = useAuthStore((s) => s.logout);
  const forcePasswordChange = useAuthStore((s) => s.forcePasswordChange);

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
  // Nested-discovery hard limits (hot-reloadable, no restart needed)
  const [maxNestedDepth, setMaxNestedDepth] = useState('5');
  const [maxResponseNodes, setMaxResponseNodes] = useState('1024');
  const [maxCacheEntries, setMaxCacheEntries] = useState('5000');
  const [maxResponseBytes, setMaxResponseBytes] = useState('1048576');
  const [maxFetchFanOut, setMaxFetchFanOut] = useState('8');
  const [savingLimits, setSavingLimits] = useState(false);
  const [nestedModalOpen, setNestedModalOpen] = useState(false);
  // Track the click origin so the Modal's open animation expands from
  // the button the user just clicked (matches NodesPage's UserModal /
  // NodeModal pattern). Falls back to undefined → centred default.
  const [nestedAnimateFrom, setNestedAnimateFrom] = useState<{ x: number; y: number } | undefined>();

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
      setMaxNestedDepth(String(settings.max_nested_depth || 5));
      setMaxResponseNodes(String(settings.max_response_nodes || 1024));
      setMaxCacheEntries(String(settings.max_cache_entries || 5000));
      setMaxResponseBytes(String(settings.max_response_bytes || 1048576));
      setMaxFetchFanOut(String(settings.max_fetch_fan_out || 8));
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

  const handleSaveLimits = async () => {
    setSavingLimits(true);
    try {
      await api.updateUISettings({
        max_nested_depth: parseInt(maxNestedDepth) || 5,
        max_response_nodes: parseInt(maxResponseNodes) || 1024,
        max_cache_entries: parseInt(maxCacheEntries) || 5000,
        max_response_bytes: parseInt(maxResponseBytes) || 1048576,
        max_fetch_fan_out: parseInt(maxFetchFanOut) || 8,
      });
      toast.success(t('settings.limitsSavedHot'));
      queryClient.invalidateQueries({ queryKey: ['settings'] });
      setNestedModalOpen(false);
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setSavingLimits(false); }
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
    <div className="hy-page">
      <Tabs
        items={[
          { key: 'web', label: t('settings.web'), disabled: forcePasswordChange },
          { key: 'system', label: t('settings.system') },
          { key: 'upgrade', label: t('settings.upgrade'), disabled: forcePasswordChange },
        ]}
        activeKey={activeTab}
        onChange={(key) => { if (!forcePasswordChange || key === 'system') setActiveTab(key as any); }}
      />

      <TabPanel fill activeKey={activeTab} keys={['web', 'system', 'upgrade']}>
        {activeTab === 'system' ? (
          <>
            {forcePasswordChange && (
              <div className="hy-alert-warn">{t('settings.forcePasswordChange')}</div>
            )}

            {/* Single System card: DNS inline-save + buttons opening
                modal sub-menus for nested limits / change password. */}
            <Card fill={1} title={t('settings.system')}>
              <div style={{ maxWidth: 480, display: 'flex', flexDirection: 'column', gap: 18 }}>
                {!forcePasswordChange && (
                  <FormGroup label={t('settings.dns')}>
                    {/* Input keeps stock styling; save action sits beside it
                        as a hy-circle-btn (same component family used in TLS
                        sub-menu's CA-key generate button). */}
                    <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                      <div style={{ flex: 1 }}>
                        <Input
                          value={dns}
                          onChange={(e) => setDns(e.target.value)}
                          placeholder="8.8.8.8,1.1.1.1"
                        />
                      </div>
                      <button
                        className="hy-circle-btn"
                        onClick={handleSaveDns}
                        disabled={savingDns}
                        aria-label={t('app.save')}
                        title={t('app.save')}
                      >
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor"
                             strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/>
                          <polyline points="17 21 17 13 7 13 7 21"/>
                          <polyline points="7 3 7 8 15 8"/>
                        </svg>
                      </button>
                    </div>
                  </FormGroup>
                )}

                {!forcePasswordChange && (
                  <>
                    <div className="section-divider" style={{ cursor: 'default' }}>{t('settings.nestedLimits')}</div>
                    <Button
                      onClick={(e) => {
                        setNestedAnimateFrom({ x: e.clientX, y: e.clientY });
                        setNestedModalOpen(true);
                      }}
                      style={{ justifyContent: 'space-between' }}
                      fullWidth
                    >
                      <span>{t('settings.openNestedLimits')}</span>
                      <span style={{ color: 'var(--text-muted)' }}>›</span>
                    </Button>
                  </>
                )}

                {/* Change Password — inline within the System card, separated
                    by a section divider. Always visible (the only path during
                    forced password change). */}
                <div className="section-divider" style={{ cursor: 'default' }}>{t('settings.changePassword')}</div>
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

            {/* Sub-menu: Nested-discovery limits (modal). */}
            <Modal
              open={nestedModalOpen}
              onClose={() => setNestedModalOpen(false)}
              title={t('settings.nestedLimits')}
              animateFrom={nestedAnimateFrom}
              footer={
                <>
                  <Button onClick={() => setNestedModalOpen(false)}>{t('app.cancel')}</Button>
                  <Button variant="primary" onClick={handleSaveLimits} loading={savingLimits}>{t('app.save')}</Button>
                </>
              }
            >
              <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
                <div style={{ fontSize: 12, color: 'var(--text-muted)', lineHeight: 1.5 }}>
                  {t('settings.nestedLimitsDesc')}
                </div>
                <FormGroup label={t('settings.maxNestedDepth')}>
                  <Input type="number" min={1} max={10} value={maxNestedDepth} onChange={(e) => setMaxNestedDepth(e.target.value)} />
                </FormGroup>
                <FormGroup label={t('settings.maxResponseNodes')}>
                  <Input type="number" min={1} max={65536} value={maxResponseNodes} onChange={(e) => setMaxResponseNodes(e.target.value)} />
                </FormGroup>
                <FormGroup label={t('settings.maxCacheEntries')}>
                  <Input type="number" min={1} max={100000} value={maxCacheEntries} onChange={(e) => setMaxCacheEntries(e.target.value)} />
                </FormGroup>
                <FormGroup label={t('settings.maxResponseBytes')}>
                  <Input type="number" min={1024} max={16777216} value={maxResponseBytes} onChange={(e) => setMaxResponseBytes(e.target.value)} suffix="B" />
                </FormGroup>
                <FormGroup label={t('settings.maxFetchFanOut')}>
                  <Input type="number" min={1} max={64} value={maxFetchFanOut} onChange={(e) => setMaxFetchFanOut(e.target.value)} />
                </FormGroup>
                <div style={{ fontSize: 11, color: 'var(--text-muted)' }}>{t('settings.hotReload')}</div>
              </div>
            </Modal>

          </>
        ) : activeTab === 'web' ? (
          <Card fill={1} title={t('settings.webUi')}>
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
            {/* Upgrade Binary — only in Docker. Shares 2:1 height ratio
                with the Backup card below so the licence + dependency
                list has room to breathe on taller viewports. */}
            <Card fill={2} title={t('settings.upgradeTitle')}>
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
                <LicensePanel />
              </div>
            </Card>

            {/* Backup & Restore */}
            <Card fill={1} title={t('settings.backup')}>
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
