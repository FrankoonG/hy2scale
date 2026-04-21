import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, FormGroup, Input, Toggle, Badge, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import { useNodeStore } from '@/store/node';

export default function Hy2Tab() {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();
  const node = useNodeStore((s) => s.node);

  const serverListen = node?.server?.listen || '0.0.0.0:5565';
  const serverEnabled = !!node?.server?.listen;

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
    <Card fill={1} title={t('hy2.title')}>
      <div style={{ maxWidth: 400, display: 'flex', flexDirection: 'column', gap: 16 }}>
        <div style={{ display: 'flex', gap: 14, alignItems: 'flex-start' }}>
          <div style={{ flex: '0 0 auto' }}>
            <FormGroup label={t('hy2.serverStatus')}>
              <div style={{ paddingTop: 6, pointerEvents: 'none', opacity: 0.8 }}>
                <Toggle checked={serverEnabled} onChange={() => {}} />
              </div>
            </FormGroup>
          </div>
          <div style={{ flex: 1, minWidth: 0 }}>
            <FormGroup label={t('hy2.listen')}>
              <Input value={serverListen} readOnly disabled />
            </FormGroup>
          </div>
        </div>

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
