import { createContext, useContext, useMemo, type ReactNode } from 'react';

/** Every built-in word the framework shows; the app supplies translations. */
export interface UiLabels {
  // tables
  columns: string;
  reset: string;
  perPage: string;
  total: (n: number) => string;
  prev: string;
  next: string;
  // controls
  generate: string;
  showPassword: string;
  hidePassword: string;
  clear: string;
  menu: string;
  confirm: string;
  cancel: string;
  replaceFile: string;
  search: string;
  noResults: string;
}

const defaults: UiLabels = {
  columns: 'Columns',
  reset: 'Reset',
  perPage: 'Per page',
  total: (n) => `${n} total`,
  prev: 'Previous',
  next: 'Next',
  generate: 'Generate',
  showPassword: 'Show',
  hidePassword: 'Hide',
  clear: 'Clear',
  menu: 'Menu',
  confirm: 'Confirm',
  cancel: 'Cancel',
  replaceFile: 'Click or drop another file to replace',
  search: 'Search',
  noResults: 'No matches',
};

const Ctx = createContext<UiLabels>(defaults);

/** Supplies the (translated) words framework components use. */
export function UiLabelsProvider({ labels, children }: { labels: Partial<UiLabels>; children: ReactNode }) {
  const merged = useMemo(() => ({ ...defaults, ...labels }), [labels]);
  return <Ctx.Provider value={merged}>{children}</Ctx.Provider>;
}

export function useUiLabels(): UiLabels {
  return useContext(Ctx);
}
