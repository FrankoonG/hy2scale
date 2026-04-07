import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, FormGroup, FormGrid, Input, Toggle, Badge, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import { useNodeStore } from '@/store/node';

export default function Hy2Tab() {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();
  const node = useNodeStore((s) => s.node);

  const serverListen = node?.server?.listen;
  const serverPort = serverListen?.replace(/.*:/, '') || '5565';
  const serverStatus = serverListen ? 'Enabled' : 'Disabled';

  const handleAuthToggle = async () => {
    try {
      await api.updateNode({ hy2_user_auth: !node?.hy2_user_auth } as any);
      toast.success(node?.hy2_user_auth ? t('hy2.authDisabled') : t('hy2.authEnabled'));
      queryClient.invalidateQueries({ queryKey: ['node'] });
    } catch (e: any) {
      toast.error(String(e.message || e));
    }
  };

  return (
    <Card title={t('hy2.title')}>
      <div style={{ maxWidth: 400, display: 'flex', flexDirection: 'column', gap: 16 }}>
        <FormGrid>
          <FormGroup label={t('hy2.port')}>
            <Input value={serverPort} readOnly disabled />
          </FormGroup>
          <FormGroup label={t('hy2.serverStatus')}>
            <Input value={serverStatus} readOnly disabled />
          </FormGroup>
        </FormGrid>

        <div style={{ fontSize: 12, color: 'var(--text-muted)', lineHeight: 1.6 }}>
          {t('hy2.readOnlyNote')}
        </div>

        <FormGroup label={t('hy2.allowUserAuth')}>
          <Toggle checked={node?.hy2_user_auth || false} onChange={handleAuthToggle} />
          <div style={{ fontSize: 12, color: 'var(--text-muted)', lineHeight: 1.6, marginTop: 8 }}>
            {t('hy2.userAuthDescLong')}
          </div>
        </FormGroup>
      </div>
    </Card>
  );
}
