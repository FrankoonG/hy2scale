import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import * as api from '@/api';

/**
 * LicensePanel — renders the project's umbrella licence, the
 * hand-maintained native components listed by the server, and the full
 * Go module dependency
 * list pulled from debug.BuildInfo on the server. Lives inside the
 * Upgrade card so the compliance-relevant info sits alongside the
 * "what version am I running" display.
 */
export default function LicensePanel() {
  const { t } = useTranslation();
  const { data } = useQuery({ queryKey: ['buildInfo'], queryFn: api.getBuildInfo, staleTime: 60_000 });
  if (!data) return null;
  return (
    <div className="hy-license-panel">
      <div className="hy-license-header">
        <span className="hy-license-label">{t('settings.license')}</span>
        <code className="hy-license-value">{data.license}</code>
        {data.repository && (
          <a className="hy-license-link" href={data.repository} target="_blank" rel="noopener noreferrer">{t('settings.sourceCode')}</a>
        )}
      </div>
      <div className="hy-license-hint">{t('settings.licenseHint')}</div>
      {data.natives && data.natives.length > 0 && (
        <details open>
          <summary className="hy-license-summary">{t('settings.nativeComponents')} · {data.natives.length}</summary>
          <div className="hy-license-table">
            {data.natives.map((n) => (
              <div key={n.name} className="hy-license-row">
                <span className="hy-license-name">{n.source ? <a href={n.source} target="_blank" rel="noopener noreferrer">{n.name}</a> : n.name}</span>
                <span className="hy-license-ver">{n.version}</span>
                <span className="hy-license-lic">{n.license}</span>
              </div>
            ))}
          </div>
        </details>
      )}
      {data.go_deps && data.go_deps.length > 0 && (
        <details>
          <summary className="hy-license-summary">{t('settings.goDependencies')} · {data.go_deps.length}</summary>
          <div className="hy-license-table hy-license-table--compact">
            {data.go_deps.map((d) => (
              <div key={d.path} className="hy-license-row">
                <span className="hy-license-name">{d.path}</span>
                <span className="hy-license-ver">{d.version}</span>
              </div>
            ))}
          </div>
        </details>
      )}
    </div>
  );
}
