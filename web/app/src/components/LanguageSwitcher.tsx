import i18n from '@/i18n';
import { DropdownMenu } from '@hy2scale/ui';

const languages = [
  { code: 'en', name: 'English' },
  { code: 'ko', name: '한국어' },
];

export default function LanguageSwitcher() {
  const currentLang = i18n.language?.startsWith('ko') ? 'ko' : 'en';
  const currentName = languages.find((l) => l.code === currentLang)?.name || currentLang;

  return (
    <DropdownMenu
      trigger={
        <button className="hy-lang-btn">
          <span>{currentName}</span>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" width="14" height="14">
            <polyline points="9 18 15 12 9 6" />
          </svg>
        </button>
      }
      items={languages.map((l) => ({
        key: l.code,
        label: <span style={{ fontWeight: l.code === currentLang ? 600 : undefined, color: l.code === currentLang ? 'var(--primary)' : undefined }}>{l.name}</span>,
        onClick: () => i18n.changeLanguage(l.code),
      }))}
    />
  );
}
