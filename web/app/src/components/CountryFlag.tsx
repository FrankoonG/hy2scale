import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Tooltip } from '@hy2scale/ui';
import type { GeoInfo } from '@/api/storage';
import { useGeo, hostOf, isIPLiteral } from '@/hooks/useGeo';

// Flags of public addresses, from this node's local GeoIP databases (Settings
// → Storage); without a database no flag is shown. Hovering the flag shows
// the location. The flag set (country-flag-icons, MIT) is one lazy chunk,
// loaded on first use.

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
/** Country name in the UI language (the databases have no Korean names). */
export function countryName(code: string, lang: string): string {
  if (!names.has(lang)) {
    try { names.set(lang, new Intl.DisplayNames([lang], { type: 'region' })); } catch { names.set(lang, null); }
  }
  try { return names.get(lang)?.of(code) || code; } catch { return code; }
}

const valid = (c?: string): c is string => !!c && /^[A-Z]{2}$/.test(c);

/** Location details of an address (the flag's hover card). */
export function GeoDetails({ ip, geo }: { ip?: string; geo: GeoInfo }) {
  const { t, i18n } = useTranslation();
  const rows: [string, string][] = [];
  if (valid(geo.country)) rows.push([t('geo.country'), `${countryName(geo.country, i18n.language)} (${geo.country})`]);
  if (geo.region) rows.push([t('geo.region'), geo.region]);
  if (geo.city) rows.push([t('geo.city'), geo.postal ? `${geo.city} ${geo.postal}` : geo.city]);
  if (geo.asn || geo.org) rows.push([t('geo.network'), [geo.asn ? `AS${geo.asn}` : '', geo.org || ''].filter(Boolean).join(' ')]);
  if (geo.tz) rows.push([t('geo.timezone'), geo.tz]);
  if (geo.lat !== undefined && geo.lon !== undefined) rows.push([t('geo.coords'), `${geo.lat.toFixed(2)}, ${geo.lon.toFixed(2)}`]);
  return (
    <div className="geo-card" data-testid="geo-card">
      {ip && <div className="geo-card-ip">{ip}</div>}
      {rows.map(([k, v]) => (
        <div className="geo-card-row" key={k}><span>{k}</span><span>{v}</span></div>
      ))}
    </div>
  );
}

/** The flag of a two-letter country code, with `geo` details on hover. */
export function CountryFlag({ code, ip, geo }: { code?: string; ip?: string; geo?: GeoInfo }) {
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
  // Reserve the slot while the flag set loads, so the address does not shift.
  if (!src) return flags ? null : <span className="cflag cflag-wait" aria-hidden />;
  const img = <img className="cflag" src={src} alt={code} data-country={code} draggable={false} />;
  if (!geo) return <span title={countryName(code, i18n.language)}>{img}</span>;
  return <Tooltip content={<GeoDetails ip={ip} geo={geo} />}>{img}</Tooltip>;
}

/** The flag of an address ("ip", "ip:port", "[v6]:port"); nothing when unknown. */
export function IPFlag({ addr }: { addr?: string }) {
  const { i18n } = useTranslation();
  const ip = hostOf(addr);
  const geo = useGeo(ip && isIPLiteral(ip) ? ip : undefined, i18n.language);
  if (!geo || !geo.country) return null;
  return <CountryFlag code={geo.country} ip={ip} geo={geo} />;
}

/** A public address with its country flag on the left. */
export function PublicIP({ ip, className, title, testId }: {
  ip: string; className?: string; title?: string; testId?: string;
}) {
  return (
    <span className={className} title={title} data-testid={testId}>
      <IPFlag addr={ip} />{ip}
    </span>
  );
}
