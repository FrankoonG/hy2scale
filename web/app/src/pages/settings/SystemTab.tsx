import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Card, Button, Input, PasswordInput, Toggle, Select, FormGroup, FormStack, Hint, Collapsible, SectionDivider,
  useConfirm, useToast,
} from '@hy2scale/ui';
import * as api from '@/api';
import { getBasePath } from '@/api/client';
import { OrderedList } from '@/components/OrderedList';
import type { PasswordForm, SystemForm } from './forms';

/** Restart the node, then reload once it answers again. */
function useRestart() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const [restarting, setRestarting] = useState(false);
  const restart = async () => {
    const ok = await confirm({
      title: t('settings.restart'),
      message: t('settings.restartConfirm'),
      danger: true, confirmText: t('settings.restart'), cancelText: t('app.cancel'),
    });
    if (!ok) return;
    setRestarting(true);
    try {
      await api.restartNode();
    } catch (e: any) {
      toast.error(String(e.message || e));
      setRestarting(false);
      return;
    }
    toast.info(t('settings.restarting'));
    // Reload once the node answers again (seen down, or after 5 s if the
    // restart was quicker than the first poll).
    const t0 = Date.now();
    let wentDown = false;
    while (Date.now() - t0 < 120_000) {
      await new Promise((r) => setTimeout(r, 1000));
      let up = false;
      try {
        up = (await fetch(getBasePath() + '/api/build-id', { cache: 'no-store' })).ok;
      } catch { /* down */ }
      if (!up) wentDown = true;
      else if (wentDown || Date.now() - t0 > 5000) {
        window.location.reload();
        return;
      }
    }
    toast.error(t('settings.restartTimeout'));
    setRestarting(false);
  };
  return { restart, restarting };
}

/** Settings → System: node-wide options, password, restart. */
export default function SystemTab({ sys, pw, forcePasswordChange }: {
  sys: SystemForm;
  pw: PasswordForm;
  forcePasswordChange: boolean;
}) {
  const { t } = useTranslation();
  const { restart, restarting } = useRestart();
  const { v, set } = sys;
  const num = (k: Parameters<typeof set>[0], props: { min: number; max?: number; suffix?: string }) => (
    <Input type="number" min={props.min} max={props.max} suffix={props.suffix}
      value={v[k] as string} onChange={(e) => set(k, e.target.value as never)} />
  );

  return (
    <>
      {forcePasswordChange && <div className="hy-alert-warn">{t('settings.forcePasswordChange')}</div>}

      <Card fill={1} title={t('settings.system')}>
        <FormStack maxWidth={480} gap={18}>
          {!forcePasswordChange && (
            <>
              <FormGroup label={t('settings.dns')}>
                <Input value={v.dns} onChange={(e) => set('dns', e.target.value)} placeholder="8.8.8.8,1.1.1.1" />
              </FormGroup>

              <Collapsible title={t('settings.dnsResolver')} open={sys.open.resolver} onToggle={sys.toggle('resolver')}>
                <Hint className="hint-block">{t('settings.dnsResolverDesc')}</Hint>
                <FormGroup label={t('settings.dnsResolverEnabled')}>
                  <Toggle checked={v.dns_resolver_enabled} onChange={(e) => set('dns_resolver_enabled', e.target.checked)} />
                </FormGroup>
                <FormGroup label={t('settings.dnsResolverCacheTtl')}>{num('dns_resolver_cache_ttl', { min: 0, max: 86400, suffix: 's' })}</FormGroup>
                <FormGroup label={t('settings.dnsResolverNegativeTtl')}>{num('dns_resolver_negative_ttl', { min: 0, max: 3600, suffix: 's' })}</FormGroup>
                <FormGroup label={t('settings.dnsResolverCacheSize')}>{num('dns_resolver_cache_size', { min: 1, max: 65536 })}</FormGroup>
                <FormGroup label={t('settings.dnsResolverQueryTimeout')}>{num('dns_resolver_query_timeout_ms', { min: 100, max: 30000, suffix: 'ms' })}</FormGroup>
              </Collapsible>

              <Collapsible title={t('settings.publicIp')} open={sys.open.publicIp} onToggle={sys.toggle('publicIp')} testId="public-ip-section">
                <FormGroup label={t('settings.publicIpMethod')}>
                  <Select
                    value={v.public_method}
                    onChange={(e) => set('public_method', e.target.value === 'peer+public' ? 'peer+public' : 'public')}
                    options={[
                      { value: 'public', label: t('settings.publicIpMethodPublic') },
                      { value: 'peer+public', label: t('settings.publicIpMethodPeer') },
                    ]}
                  />
                </FormGroup>
                <FormGroup label={t('settings.publicIpInterval')}>
                  <Input type="number" min={60} value={v.public_interval} onChange={(e) => set('public_interval', e.target.value)} suffix="s" data-testid="public-ip-interval" />
                </FormGroup>
                <FormGroup label={t('settings.publicIpServers')} hint={t('settings.publicIpServersHint')}>
                  <OrderedList items={v.public_servers} onChange={(items) => set('public_servers', items)} options={[]}
                    placeholder="stun:host:3478 / https://..." addLabel={t('settings.publicIpAddServer')}
                    deleteTitle={t('app.delete')} testId="public-ip-servers" />
                </FormGroup>
              </Collapsible>

              <FormGroup label={t('settings.relayPassthrough')} hint={t('settings.relayPassthroughDesc')}>
                <Toggle checked={v.relay_admin_passthrough} onChange={(e) => set('relay_admin_passthrough', e.target.checked)} />
              </FormGroup>

              <FormGroup label={t('settings.acceptPunch')} hint={t('settings.acceptPunchDesc')}>
                <Toggle checked={v.accept_punch} onChange={(e) => set('accept_punch', e.target.checked)} data-testid="accept-punch" />
              </FormGroup>

              <FormGroup label={t('settings.pathLossGrace')} hint={t('settings.pathLossGraceDesc')}>
                {num('path_loss_grace_s', { min: 3, max: 300, suffix: 's' })}
              </FormGroup>

              <Collapsible title={t('settings.nestedLimits')} open={sys.open.nested} onToggle={sys.toggle('nested')}>
                <Hint className="hint-block">{t('settings.nestedLimitsDesc')}</Hint>
                <FormGroup label={t('settings.maxNestedDepth')}>{num('max_nested_depth', { min: 1, max: 10 })}</FormGroup>
                <FormGroup label={t('settings.maxResponseNodes')}>{num('max_response_nodes', { min: 1, max: 65536 })}</FormGroup>
                <FormGroup label={t('settings.maxCacheEntries')}>{num('max_cache_entries', { min: 1, max: 100000 })}</FormGroup>
                <FormGroup label={t('settings.maxResponseBytes')}>{num('max_response_bytes', { min: 1024, max: 16777216, suffix: 'B' })}</FormGroup>
                <FormGroup label={t('settings.maxFetchFanOut')}>{num('max_fetch_fan_out', { min: 1, max: 64 })}</FormGroup>
              </Collapsible>

              <div className="button-row">
                <Button variant="primary" onClick={sys.save} loading={sys.saving}>{t('app.save')}</Button>
                <Button variant="ghost" onClick={sys.restoreNestedDefaults}>{t('settings.restoreDefaults')}</Button>
              </div>
            </>
          )}

          {/* Always shown: the only section during a forced password change. */}
          <SectionDivider>{t('settings.changePassword')}</SectionDivider>
          <FormGroup label={t('settings.currentPassword')} required>
            <PasswordInput value={pw.curPw} onChange={(e) => pw.setCurPw(e.target.value)} />
          </FormGroup>
          <FormGroup label={t('settings.newUsernameOpt')}>
            <Input value={pw.newUser} onChange={(e) => pw.setNewUser(e.target.value)} />
          </FormGroup>
          <FormGroup label={t('settings.newPassword')}>
            <PasswordInput value={pw.newPw} onChange={(e) => pw.setNewPw(e.target.value)} onGenerate={(p) => { pw.setNewPw(p); pw.setConfirmPw(p); }} />
          </FormGroup>
          <FormGroup label={t('settings.confirmPassword')}>
            <PasswordInput value={pw.confirmPw} onChange={(e) => pw.setConfirmPw(e.target.value)} />
          </FormGroup>
          <Button variant="primary" className="align-start" onClick={pw.save} loading={pw.saving}>{t('settings.update')}</Button>

          <SectionDivider>{t('settings.restart')}</SectionDivider>
          <Button variant="danger" className="align-start" onClick={restart} loading={restarting}>{t('settings.restart')}</Button>
        </FormStack>
      </Card>
    </>
  );
}
