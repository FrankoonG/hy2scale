import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import {
  Card, Badge, DetailGrid, SegmentedControl, Table,
  IconGithub, IconBookOpen, IconCircleDot, IconTag, IconPackage, IconExternalLink,
  type Column, type IconComponent,
} from '@hy2scale/ui';
import * as api from '@/api';
import type { BuildInfoDep, BuildInfoNative } from '@/api';
import BrandLogo from '@/components/login/BrandLogo';

const DOCKER_HUB = 'https://hub.docker.com/r/frankoong/hy2scale';
const FIRST_YEAR = 2026;

/** A Go module version; a pseudo-version (an untagged commit) as "hash · date". */
function modVersion(v: string) {
  const m = /-(?:0\.)?(\d{4})(\d{2})(\d{2})\d{6}-([0-9a-f]{12})$/.exec(v);
  return m ? `${m[4].slice(0, 7)} · ${m[1]}-${m[2]}-${m[3]}` : v;
}

/** Settings → About: the project, this build, what it is built on, licences. */
export default function AboutTab() {
  const { t } = useTranslation();
  const { data } = useQuery({ queryKey: ['buildInfo'], queryFn: api.getBuildInfo, staleTime: 60_000 });
  const [list, setList] = useState<'natives' | 'go'>('natives');
  if (!data) return null;

  const repo = data.repository;
  const links: { key: string; label: string; href: string; icon: IconComponent }[] = [
    { key: 'source', label: t('about.source'), href: repo, icon: IconGithub },
    { key: 'wiki', label: t('about.wiki'), href: `${repo}/wiki`, icon: IconBookOpen },
    { key: 'issues', label: t('about.issues'), href: `${repo}/issues`, icon: IconCircleDot },
    { key: 'releases', label: t('about.releases'), href: `${repo}/releases`, icon: IconTag },
    { key: 'docker', label: 'Docker Hub', href: DOCKER_HUB, icon: IconPackage },
  ];

  const commit = data.commit ? data.commit.slice(0, 12) : '';
  const build = [
    [t('about.version'), <span className="mono">{data.version}</span>],
    ...(commit ? [[t('about.commit'), (
      <span className="about-commit">
        <a className="mono" href={`${repo}/commit/${data.commit}`} target="_blank" rel="noopener noreferrer">{commit}</a>
        {data.modified && <Badge variant="orange">{t('about.modified')}</Badge>}
      </span>
    )]] : []),
    ...(data.commit_time ? [[t('about.commitTime'), <span className="mono">{data.commit_time.replace('T', ' ').replace('Z', ' UTC')}</span>]] : []),
    ...(data.go_version ? [['Go', <span className="mono">{data.go_version}</span>]] : []),
    ...(data.platform ? [[t('about.platform'), <span className="mono">{data.platform}</span>]] : []),
  ] as [string, ReactNode][];

  const extLink = (href: string, text: string) => (
    href ? <a href={href} target="_blank" rel="noopener noreferrer">{text}</a> : text
  );
  const nativeCols: Column<BuildInfoNative>[] = [
    { key: 'name', title: t('about.component'), sortValue: (n) => n.name, render: (n) => extLink(n.source, n.name) },
    { key: 'version', title: t('about.version'), render: (n) => <span className="mono">{n.version}</span> },
    { key: 'license', title: t('settings.license'), sortValue: (n) => n.license, render: (n) => <span className="mono">{n.license}</span> },
  ];
  const goCols: Column<BuildInfoDep>[] = [
    { key: 'path', title: t('about.module'), sortValue: (d) => d.path, render: (d) => <span className="mono">{d.path}</span> },
    { key: 'version', title: t('about.version'), render: (d) => <span className="mono" title={d.version}>{modVersion(d.version)}</span> },
  ];
  const year = new Date().getFullYear();

  return (
    <div className="about" data-testid="about">
      <section className="about-hero">
        <div className="about-logo"><BrandLogo size={88} /></div>
        <div className="about-title">
          <h2 className="about-name">HY2 SCALE</h2>
          <p className="about-tagline">{t('about.tagline')}</p>
          <div className="about-chips">
            <span className="about-chip" data-testid="about-version">v{data.version}</span>
            <span className="about-chip">{data.license}</span>
          </div>
        </div>
        <nav className="about-links">
          {links.map(({ key, label, href, icon: Icon }) => (
            <a key={key} className="about-link" href={href} target="_blank" rel="noopener noreferrer" data-testid={`about-link-${key}`}>
              <Icon size={15} />
              <span>{label}</span>
            </a>
          ))}
        </nav>
      </section>

      <div className="about-cols">
        <Card title={t('about.build')}>
          <DetailGrid variant="plain" columns={2} items={build} testId="about-build" />
        </Card>
        {data.credits && data.credits.length > 0 && (
          <Card title={t('about.builtOn')}>
            <ul className="about-credits" data-testid="about-credits">
              {data.credits.map((c) => (
                <li key={c.name}>
                  <a className="about-credit-name" href={c.source} target="_blank" rel="noopener noreferrer">
                    {c.name}<IconExternalLink size={11} />
                  </a>
                  <span className="about-credit-ver mono" title={c.version}>{modVersion(c.version)}</span>
                  <span className="about-credit-lic">{c.license}</span>
                </li>
              ))}
            </ul>
          </Card>
        )}
      </div>

      <Card
        title={t('about.licenses')}
        actions={(
          <SegmentedControl
            value={list}
            onChange={setList}
            items={[
              { key: 'natives', label: t('settings.nativeComponents'), count: data.natives?.length ?? 0, testId: 'about-natives' },
              { key: 'go', label: t('settings.goDependencies'), count: data.go_deps?.length ?? 0, testId: 'about-go' },
            ]}
          />
        )}
      >
        {list === 'natives'
          ? <Table id="about.natives" columns={nativeCols} data={data.natives || []} rowKey={(n) => n.name} paginate={false} />
          : <Table id="about.go" columns={goCols} data={data.go_deps || []} rowKey={(d) => d.path} />}
        <p className="about-note">{t('settings.licenseHint')}</p>
      </Card>

      <footer className="about-foot">
        <span>© {year > FIRST_YEAR ? `${FIRST_YEAR}–${year}` : FIRST_YEAR} {t('about.copyright')}</span>
        <span>{t('about.notAffiliated')}</span>
      </footer>
    </div>
  );
}
