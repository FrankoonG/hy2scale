import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Card, Button, Input, Toggle, Select, FormGroup, FormStack } from '@hy2scale/ui';
import * as api from '@/api';
import type { WebForm } from './forms';

/** Settings → Web: the admin UI's own listener. */
export default function WebTab({ web }: { web: WebForm }) {
  const { t } = useTranslation();
  const { data: certs = [] } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts });
  const certOptions = [
    { value: '', label: t('settings.httpsCertDefault') },
    ...certs.map((c) => ({ value: c.id, label: c.name || c.id })),
  ];
  return (
    <Card fill={1} title={t('settings.webUi')}>
      <FormStack maxWidth={400}>
        <FormGroup label={t('settings.listenAddress')}>
          <Input value={web.listen} onChange={(e) => web.setListen(e.target.value)} placeholder=":5565" />
        </FormGroup>
        <FormGroup label={t('settings.basePath')} hint={t('settings.restartRequired')}>
          <Input value={web.basePath} onChange={(e) => web.setBasePath(e.target.value)} placeholder="/scale" />
        </FormGroup>
        <FormGroup label={t('settings.forceHttps')}>
          {/* always on */}
          <Toggle checked disabled readOnly data-testid="web-force-https" />
        </FormGroup>
        <FormGroup label={t('settings.httpsCert')}>
          <Select value={web.httpsCertId} onChange={(e) => web.setHttpsCertId(e.target.value)} options={certOptions} />
        </FormGroup>
        <FormGroup label={t('settings.sessionTimeout')}>
          <Input type="number" value={web.sessionTimeout} onChange={(e) => web.setSessionTimeout(e.target.value)} suffix="h" />
        </FormGroup>
        <Button variant="primary" className="align-start" onClick={web.save} loading={web.saving}>{t('app.save')}</Button>
      </FormStack>
    </Card>
  );
}
