import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { Card, FormGroup, FormStack, FormRow, Hint, Input, Toggle, useToast } from '@hy2scale/ui';
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
      <FormStack maxWidth={400} gap={16}>
        <FormRow>
          <FormGroup label={t('hy2.serverStatus')}>
            {/* Follows the node's server listen; edited in Nodes → this node */}
            <Toggle offset className="readonly" checked={serverEnabled} readOnly />
          </FormGroup>
          <FormGroup label={t('hy2.listen')}>
            <Input value={serverListen} readOnly disabled />
          </FormGroup>
        </FormRow>

        <Hint>{t('hy2.readOnlyNote')}</Hint>

        <FormGroup label={t('hy2.allowUserAuth')}>
          <Toggle checked={node?.hy2_user_auth || false} onChange={handleAuthToggle} />
          <Hint className="hint-gap">{t('hy2.userAuthDescLong')}</Hint>
        </FormGroup>
      </FormStack>
    </Card>
  );
}
