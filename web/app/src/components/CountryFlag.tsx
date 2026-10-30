import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';

// Flags of public addresses. The country is found by the node that owns the
// address (peers send theirs with their egress); this only draws it. The
// flag set (country-flag-icons, MIT) is one lazy chunk, loaded on first use.

type FlagSet = Record<string, string>;
let flags: FlagSet | null = null;
let loading: Promise<FlagSet> | null = null;
const urls = new Map<string, string>();

function loadFlags(): Promise<FlagSet> {
  loading ??= import('country-flag-icons/string/3x2').then((m) => (flags = m as unknown as FlagSet));
  return loading;
}

function flagURL(code: string): string | null {
  const svg = flags?.[code];
  if (!svg) return null;
  let u = urls.get(code);
  if (!u) urls.set(code, (u = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg)));
  return u;
}

const names = new Map<string, Intl.DisplayNames | null>();
function countryName(code: string, lang: string): string {
  if (!names.has(lang)) {
    try { names.set(lang, new Intl.DisplayNames([lang], { type: 'region' })); } catch { names.set(lang, null); }
  }
  try { return names.get(lang)?.of(code) || code; } catch { return code; }
}

const valid = (c?: string): c is string => !!c && /^[A-Z]{2}$/.test(c);

/** The flag of a two-letter country code; nothing for an unknown country. */
export function CountryFlag({ code }: { code?: string }) {
  const { i18n } = useTranslation();
  const [, setReady] = useState(!!flags);
  useEffect(() => {
    if (valid(code) && !flags) {
      let live = true;
      loadFlags().then(() => live && setReady(true), () => {});
      return () => { live = false; };
    }
  }, [code]);
  if (!valid(code)) return null;
  const src = flagURL(code);
  const name = countryName(code, i18n.language);
  // Reserve the slot while the flag set loads, so the address does not shift.
  if (!src) return flags ? null : <span className="cflag cflag-wait" aria-hidden />;
  return <img className="cflag" src={src} alt={code} title={name} data-country={code} draggable={false} />;
}

/** A public address with its country flag on the left. */
export function PublicIP({ ip, country, className, title, testId }: {
  ip: string; country?: string; className?: string; title?: string; testId?: string;
}) {
  return (
    <span className={className} title={title} data-testid={testId}>
      <CountryFlag code={country} />{ip}
    </span>
  );
}
