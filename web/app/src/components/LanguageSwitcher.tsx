import { useState, useRef, useEffect } from 'react';
import i18n from '@/i18n';

const languages = [
  { code: 'en', name: 'English' },
  { code: 'ko', name: '\ud55c\uad6d\uc5b4' },
];

export default function LanguageSwitcher() {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const currentLang = i18n.language?.startsWith('ko') ? 'ko' : 'en';
  const currentName = languages.find((l) => l.code === currentLang)?.name || currentLang;

  useEffect(() => {
    const handler = (e: Event) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('click', handler);
    return () => document.removeEventListener('click', handler);
  }, []);

  return (
    <div ref={ref} style={{ position: 'relative', marginLeft: 8 }}>
      <button
        onClick={() => setOpen(!open)}
        style={{
          border: 'none', background: 'none', cursor: 'pointer',
          display: 'flex', alignItems: 'center', gap: 4,
          color: 'var(--text-secondary)', fontSize: 13, fontWeight: 500,
          fontFamily: 'var(--font)', padding: '4px 0',
        }}
      >
        <span>{currentName}</span>
        <svg
          viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
          width="14" height="14"
          style={{ transition: 'transform .2s ease', transform: open ? 'rotate(90deg)' : undefined }}
        >
          <polyline points="9 18 15 12 9 6" />
        </svg>
      </button>
      {open && (
        <div style={{
          position: 'absolute', right: 0, top: 'calc(100% + 4px)',
          background: 'var(--surface)', border: '1px solid var(--border)',
          borderRadius: 'var(--radius-sm)', boxShadow: '0 4px 12px rgba(0,0,0,.12)',
          minWidth: 120, zIndex: 300, overflow: 'hidden',
        }}>
          {languages.map((l) => (
            <div
              key={l.code}
              onClick={() => { i18n.changeLanguage(l.code); setOpen(false); }}
              style={{
                padding: '8px 14px', fontSize: 13, cursor: 'pointer',
                display: 'flex', alignItems: 'center', gap: 6,
                color: l.code === currentLang ? 'var(--primary)' : undefined,
                fontWeight: l.code === currentLang ? 600 : undefined,
                background: 'transparent',
              }}
              onMouseEnter={(e) => (e.currentTarget.style.background = 'var(--primary-light)')}
              onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
            >
              {l.name}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
