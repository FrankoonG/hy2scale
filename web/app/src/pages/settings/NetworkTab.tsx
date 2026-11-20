import { useTranslation } from 'react-i18next';
import { Card, Button, Input, Toggle, Select, FormGroup, FormStack, SectionDivider } from '@hy2scale/ui';
import { OrderedList } from '@/components/OrderedList';
import type { UISettings } from '@/api';
import type { SystemForm } from './forms';

/** Settings → Network: relaying, hole punching, shared users, path grace, public IP detection. */
export default function NetworkTab({ sys, settings }: { sys: SystemForm; settings?: UISettings }) {
  const { t } = useTranslation();
  const { v, set } = sys;
  const st = settings?.mdns_state;
  const lanStatus = st === 'on' && settings?.mdns_claimed ? t('settings.lanNameOn', { name: settings.mdns_claimed })
    : st ? t(`settings.lanNameState.${st}`) : undefined;
  return (
    <Card fill={1} title={t('settings.network')}>
      <FormStack maxWidth={480} gap={18}>
        <FormGroup label={t('settings.relayPassthrough')} hint={t('settings.relayPassthroughDesc')}>
          <Toggle checked={v.relay_admin_passthrough} onChange={(e) => set('relay_admin_passthrough', e.target.checked)} />
        </FormGroup>
        <FormGroup label={t('settings.acceptPunch')} hint={t('settings.acceptPunchDesc')}>
          <Toggle checked={v.accept_punch} onChange={(e) => set('accept_punch', e.target.checked)} data-testid="accept-punch" />
        </FormGroup>
        <FormGroup label={t('settings.acceptSharedUsers')} hint={t('settings.acceptSharedUsersDesc')}>
          <Toggle checked={v.accept_shared_users} onChange={(e) => set('accept_shared_users', e.target.checked)} data-testid="accept-shared-users" />
        </FormGroup>
        <FormGroup label={t('settings.pathLossGrace')} hint={t('settings.pathLossGraceDesc')}>
          <Input type="number" min={3} max={300} suffix="s" value={v.path_loss_grace_s}
            onChange={(e) => set('path_loss_grace_s', e.target.value)} />
        </FormGroup>

        <SectionDivider>{t('settings.publicHostSection')}</SectionDivider>
        <FormGroup label={t('settings.publicHost')} hint={t('settings.publicHostHint')}>
          <Input value={v.public_host} onChange={(e) => set('public_host', e.target.value)}
            placeholder={settings?.public_host_detected || t('settings.publicHostNone')} data-testid="public-host" />
        </FormGroup>
        <FormGroup label={t('settings.lanName')} hint={lanStatus}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <Toggle checked={v.mdns_enabled} onChange={(e) => set('mdns_enabled', e.target.checked)} data-testid="mdns-enabled" />
            <div style={{ flex: 1, minWidth: 0 }}>
              <Input value={v.mdns_name} onChange={(e) => set('mdns_name', e.target.value.toLowerCase())}
                placeholder={settings?.mdns_label || ''} suffix=".local" disabled={!v.mdns_enabled} data-testid="mdns-name" />
            </div>
          </div>
        </FormGroup>

        <SectionDivider>{t('settings.publicIp')}</SectionDivider>
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

        <Button variant="primary" className="align-start" onClick={() => sys.save('network')} loading={sys.saving === 'network'}>{t('app.save')}</Button>
      </FormStack>
    </Card>
  );
}
