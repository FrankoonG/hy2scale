import { useTranslation } from 'react-i18next';
import { Card, Button, Input, Toggle, Select, FormGroup, FormStack, SectionDivider } from '@hy2scale/ui';
import { OrderedList } from '@/components/OrderedList';
import type { SystemForm } from './forms';

/** Settings → Network: relaying, hole punching, path grace, public IP detection. */
export default function NetworkTab({ sys }: { sys: SystemForm }) {
  const { t } = useTranslation();
  const { v, set } = sys;
  return (
    <Card fill={1} title={t('settings.network')}>
      <FormStack maxWidth={480} gap={18}>
        <FormGroup label={t('settings.relayPassthrough')} hint={t('settings.relayPassthroughDesc')}>
          <Toggle checked={v.relay_admin_passthrough} onChange={(e) => set('relay_admin_passthrough', e.target.checked)} />
        </FormGroup>
        <FormGroup label={t('settings.acceptPunch')} hint={t('settings.acceptPunchDesc')}>
          <Toggle checked={v.accept_punch} onChange={(e) => set('accept_punch', e.target.checked)} data-testid="accept-punch" />
        </FormGroup>
        <FormGroup label={t('settings.pathLossGrace')} hint={t('settings.pathLossGraceDesc')}>
          <Input type="number" min={3} max={300} suffix="s" value={v.path_loss_grace_s}
            onChange={(e) => set('path_loss_grace_s', e.target.value)} />
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
