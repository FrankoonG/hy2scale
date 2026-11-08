import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { useConfirm, useToast } from '@hy2scale/ui';
import type { UserConfig } from '@/api';
import * as api from '@/api';
import { useBulkAction } from '@/hooks/useBulkAction';

/** Delete of shared users only = leave (removes them from this node only). */
export const isLeaving = (users: UserConfig[]) => users.length > 0 && users.every((u) => u.shared);

/**
 * Enable / disable, reset traffic, delete (leave) and delete everywhere on
 * users, with the Users page's confirms and toasts. Each resolves true when
 * it ran and succeeded.
 */
export function useUserActions() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const queryClient = useQueryClient();
  const bulk = useBulkAction(['users']);

  const toggle = (users: UserConfig[], enabled: boolean) => bulk(users.map((u) => u.id), (id) => api.toggleUser(id, enabled),
    { label: enabled ? t('app.bulkEnable') : t('app.bulkDisable') });

  const reset = (users: UserConfig[]) => bulk(users.map((u) => u.id), (id) => api.resetTraffic(id), {
    label: t('users.bulkResetTraffic'),
    confirm: { title: t('users.resetTitle'), message: t('users.resetConfirm'), confirmText: t('users.reset') },
  });

  const remove = (users: UserConfig[]) => bulk(users.map((u) => u.id), (id) => api.deleteUser(id), isLeaving(users) ? {
    label: t('users.leave'),
    confirm: { title: t('users.leave'), message: t('users.leaveConfirm'), confirmText: t('users.leave'), danger: true },
  } : {
    label: t('app.bulkDelete'),
    confirm: { title: t('app.bulkDelete'), message: t('app.bulkDeleteConfirm', { count: users.length }), confirmText: t('app.delete'), danger: true },
  });

  const deleteEverywhere = async (u: UserConfig): Promise<boolean> => {
    const ok = await confirm({
      title: t('users.deleteEverywhere'), message: t('users.deleteEverywhereConfirm', { name: u.username }),
      confirmText: t('users.deleteEverywhere'), cancelText: t('app.cancel'), danger: true,
    });
    if (!ok) return false;
    try {
      await api.deleteUserEverywhere(u.id);
      toast.success(t('users.deletedEverywhere', { name: u.username }));
      queryClient.invalidateQueries({ queryKey: ['users'] });
      return true;
    } catch (e: any) {
      toast.error(String(e?.message || e));
      return false;
    }
  };

  return { toggle, reset, remove, deleteEverywhere };
}
