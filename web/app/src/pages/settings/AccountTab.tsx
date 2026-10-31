import { useTranslation } from 'react-i18next';
import { Card, Button, Input, PasswordInput, FormGroup, FormStack } from '@hy2scale/ui';
import type { PasswordForm } from './forms';

/** Settings → Account: admin username and password. */
export default function AccountTab({ pw, forcePasswordChange }: { pw: PasswordForm; forcePasswordChange: boolean }) {
  const { t } = useTranslation();
  return (
    <>
      {forcePasswordChange && <div className="hy-alert-warn">{t('settings.forcePasswordChange')}</div>}
      <Card fill={1} title={t('settings.changePassword')}>
        <FormStack maxWidth={480} gap={18}>
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
        </FormStack>
      </Card>
    </>
  );
}
