import { useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { useConfirm, useToast } from '@hy2scale/ui';

export interface BulkOptions {
  /** Toast prefix ("Delete: 3"). */
  label: string;
  /** Ask first; `danger` styles the confirm button red. */
  confirm?: { title: string; message: string; confirmText: string; danger?: boolean };
}

/**
 * Runs one API call per selected key in parallel, then toasts
 * "<label>: N" and refetches `queryKey`. Errors toast their message.
 */
export function useBulkAction(queryKey: readonly unknown[]) {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const qc = useQueryClient();
  const keyStr = JSON.stringify(queryKey);
  return useCallback(async (keys: Iterable<string>, run: (key: string) => Promise<unknown>, opts: BulkOptions): Promise<boolean> => {
    const list = [...keys];
    if (opts.confirm) {
      const ok = await confirm({ ...opts.confirm, cancelText: t('app.cancel') });
      if (!ok) return false;
    }
    try {
      await Promise.all(list.map(run));
      toast.success(`${opts.label}: ${list.length}`);
      qc.invalidateQueries({ queryKey: JSON.parse(keyStr) });
      return true;
    } catch (e: any) {
      toast.error(String(e?.message || e));
      return false;
    }
  }, [confirm, toast, qc, keyStr, t]);
}
