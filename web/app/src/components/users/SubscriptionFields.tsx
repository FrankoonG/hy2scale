import { useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Input, Toggle, FormGroup, FormGrid } from '@hy2scale/ui';
import { SUB_NAME_DEFAULT, SUB_NAME_MAX, SUB_NAME_PLACEHOLDERS, formatLinkName, type SubNameValues } from './subName';

/** The user dialog's subscription settings. */
export interface SubForm {
  enabled: boolean;
  /** entry name template ('' = the default) */
  name: string;
  lan: boolean;
}

/** On/off, LAN entries and the entry name template with a preview from sample values. */
export function SubscriptionFields({ value, onChange, sample }: {
  value: SubForm;
  onChange: (v: SubForm) => void;
  sample: SubNameValues;
}) {
  const { t } = useTranslation();
  const nameRef = useRef<HTMLInputElement>(null);
  // the last cursor / selection in the name (none yet: its end)
  const caret = useRef<[number, number] | null>(null);
  const set = (patch: Partial<SubForm>) => onChange({ ...value, ...patch });

  // a placeholder goes in at the cursor
  const insert = (ph: string) => {
    const len = value.name.length;
    const token = `{${ph}}`;
    const [start, end] = (caret.current || [len, len]).map((n) => Math.min(n, len));
    const name = (value.name.slice(0, start) + token + value.name.slice(end)).slice(0, SUB_NAME_MAX);
    const at = Math.min(start + token.length, name.length);
    caret.current = [at, at];
    set({ name });
    requestAnimationFrame(() => {
      const el = nameRef.current;
      if (!el) return;
      el.focus();
      el.setSelectionRange(at, at);
    });
  };

  return (
    <>
      <FormGrid>
        <FormGroup label={t('users.subscriptionTab')}>
          <Toggle checked={value.enabled} onChange={(e) => set({ enabled: e.target.checked })} data-testid="user-sub" />
        </FormGroup>
        {value.enabled && (
          <FormGroup label={t('sub.lanEntries')}>
            <Toggle checked={value.lan} onChange={(e) => set({ lan: e.target.checked })} data-testid="user-sub-lan" />
          </FormGroup>
        )}
      </FormGrid>
      {value.enabled && (
        <FormGroup label={t('users.subName')}>
          <div className="sub-name">
            <Input ref={nameRef} value={value.name} maxLength={SUB_NAME_MAX} placeholder={SUB_NAME_DEFAULT}
              onChange={(e) => set({ name: e.target.value })}
              onSelect={(e) => { caret.current = [e.currentTarget.selectionStart ?? 0, e.currentTarget.selectionEnd ?? 0]; }}
              data-testid="user-sub-name" />
            <div className="sub-name-tokens">
              {SUB_NAME_PLACEHOLDERS.map((ph) => (
                <button key={ph} type="button" className="sub-name-token" onClick={() => insert(ph)}
                  data-testid={`user-sub-ph-${ph}`}>{`{${ph}}`}</button>
              ))}
            </div>
            <div className="sub-name-preview">
              <span className="sub-name-preview-k">{t('users.subPreview')}</span>
              <span className="sub-name-preview-v" data-testid="user-sub-preview">{formatLinkName(value.name, sample)}</span>
            </div>
          </div>
        </FormGroup>
      )}
    </>
  );
}
