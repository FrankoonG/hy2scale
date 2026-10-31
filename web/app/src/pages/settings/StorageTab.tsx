import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Card, Button, Input, Toggle, FormGroup, FormGrid, FormStack, Hint, FileDropZone, Progress, Badge, DetailGrid, SegmentedControl,
  useConfirm, useToast,
} from '@hy2scale/ui';
import {
  getStorageSettings, updateStorageSettings, wipeStorage, deleteGeoDatabase, uploadGeoDatabase, setGeoMode, updateGeoNow,
  type StorageKind, type GeoDatabase, type GeoMode,
} from '@/api/storage';
import { useGeoStatus, clearGeoCache } from '@/hooks/useGeo';
import { fmtBytes } from '@/hooks/useFormat';

type Kind = 'logs' | 'audit';

/** Settings → Storage: what this node keeps — logs, audit — and its GeoIP databases. */
export default function StorageTab() {
  const { data } = useQuery({ queryKey: ['storageSettings'], queryFn: getStorageSettings });
  return (
    <>
      <KeptCard kind="logs" cur={data?.logs} />
      <KeptCard kind="audit" cur={data?.audit} />
      <GeoCard />
    </>
  );
}

function KeptCard({ kind, cur }: { kind: Kind; cur?: StorageKind }) {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const qc = useQueryClient();
  const [enabled, setEnabled] = useState(false);
  const [days, setDays] = useState('');
  const [mb, setMb] = useState('');
  const [saving, setSaving] = useState(false);
  // Seed from the server once it answers (and after each save).
  const seed = cur ? `${cur.enabled}/${cur.retention_days}/${cur.max_mb}` : '';
  useEffect(() => {
    if (!cur) return;
    setEnabled(cur.enabled);
    setDays(String(cur.retention_days));
    setMb(String(cur.max_mb));
  }, [seed]); // eslint-disable-line react-hooks/exhaustive-deps

  const save = async (next = enabled) => {
    setSaving(true);
    try {
      await updateStorageSettings({ [kind]: { enabled: next, retention_days: Number(days) || 1, max_mb: Number(mb) || 1 } });
      await qc.invalidateQueries({ queryKey: ['storageSettings'] });
      toast.success(t('storage.saved'));
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setSaving(false);
    }
  };
  const wipe = async () => {
    if (!await confirm({ title: t(`storage.${kind}`), message: t(`storage.wipeConfirm.${kind}`), danger: true, confirmText: t('storage.wipe') })) return;
    try {
      await wipeStorage(kind);
      qc.invalidateQueries({ queryKey: ['storageSettings'] });
      toast.success(t('storage.wiped'));
    } catch (e: any) { toast.error(String(e.message || e)); }
  };

  return (
    <Card title={t(`storage.${kind}`)} actions={cur && <span className="muted" data-testid={`storage-${kind}-used`}>{t('storage.used', { size: fmtBytes(cur.used_bytes || 0) })}</span>}>
      <FormStack maxWidth={520}>
        <Hint>{t(`storage.${kind}Hint`)}</Hint>
        <FormGroup label={t('storage.keep')}>
          <Toggle checked={enabled} onChange={(e) => { setEnabled(e.target.checked); save(e.target.checked); }} data-testid={`storage-${kind}-enabled`} />
        </FormGroup>
        <FormGrid>
          <FormGroup label={t('storage.retention')}>
            <Input type="number" min={1} max={3650} value={days} onChange={(e) => setDays(e.target.value)} suffix={t('storage.days')} />
          </FormGroup>
          <FormGroup label={t('storage.maxSize')}>
            <Input type="number" min={1} value={mb} onChange={(e) => setMb(e.target.value)} suffix="MB" />
          </FormGroup>
        </FormGrid>
        {kind === 'audit' && !!cur?.dropped && <div className="storage-warn">{t('storage.dropped', { n: cur.dropped })}</div>}
        <div className="button-row">
          <Button variant="primary" onClick={() => save()} loading={saving}>{t('app.save')}</Button>
          <Button variant="danger" onClick={wipe} disabled={!cur?.used_bytes} data-testid={`storage-${kind}-wipe`}>{t('storage.wipe')}</Button>
        </div>
      </FormStack>
    </Card>
  );
}

const KIND_ORDER = ['city', 'country', 'asn'] as const;
const SOURCE_NAME: Record<string, string> = { dbip: 'DB-IP Lite', iplocate: 'IPLocate' };
const when = (s?: string) => (s ? new Date(s).toLocaleString() : '—');

function GeoCard() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const qc = useQueryClient();
  const { data: status } = useGeoStatus();
  const [file, setFile] = useState<File | null>(null);
  const [progress, setProgress] = useState<number | null>(null);
  const [switching, setSwitching] = useState(false);
  const mode: GeoMode = status?.mode || 'manual';
  const auto = status?.auto;
  const checking = mode === 'auto' && auto?.state === 'checking';

  const refresh = () => { clearGeoCache(); qc.invalidateQueries({ queryKey: ['geoStatus'] }); };
  // While an automatic check runs, follow it.
  useEffect(() => {
    if (!checking) return;
    const id = window.setInterval(refresh, 3000);
    return () => window.clearInterval(id);
  }, [checking]); // eslint-disable-line react-hooks/exhaustive-deps

  const switchMode = async (m: GeoMode) => {
    if (m === mode) return;
    setSwitching(true);
    try {
      await setGeoMode(m);
      refresh();
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setSwitching(false);
    }
  };
  const checkNow = async () => {
    try {
      await updateGeoNow();
      refresh();
    } catch (e: any) { toast.error(String(e.message || e)); }
  };
  const upload = async () => {
    if (!file) return;
    setProgress(0);
    try {
      const r = await uploadGeoDatabase(file, setProgress);
      toast.success(t('storage.geoUploaded', { type: r.type }));
      setFile(null);
      refresh();
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setProgress(null);
    }
  };
  const remove = async (db: GeoDatabase) => {
    if (!await confirm({ title: t('storage.geoip'), message: t('storage.geoDeleteConfirm', { type: db.type }), danger: true, confirmText: t('app.delete') })) return;
    try {
      await deleteGeoDatabase(db.kind);
      refresh();
    } catch (e: any) { toast.error(String(e.message || e)); }
  };

  const dbs = [...(status?.databases || [])].sort((a, b) => KIND_ORDER.indexOf(a.kind) - KIND_ORDER.indexOf(b.kind));

  return (
    <Card
      title={t('storage.geoip')}
      fill={1}
      actions={(
        <SegmentedControl<GeoMode>
          items={[
            { key: 'manual', label: t('storage.geoManual'), testId: 'geo-mode-manual' },
            { key: 'auto', label: t('storage.geoAuto'), testId: 'geo-mode-auto' },
          ]}
          value={mode}
          onChange={(m) => { if (!switching) switchMode(m); }}
        />
      )}
    >
      <FormStack maxWidth={640}>
        <Hint>{t(mode === 'auto' ? 'storage.geoAutoHint' : 'storage.geoHint')}</Hint>

        {mode === 'auto' && (
          <div className="geo-auto" data-testid="geo-auto">
            <DetailGrid
              variant="plain"
              columns={2}
              items={[
                [t('storage.geoSource'), (
                  <span data-testid="geo-auto-source">
                    {auto?.source ? SOURCE_NAME[auto.source] : '—'}
                    {auto?.source && <span className="muted"> · {t(auto.source === 'dbip' ? 'storage.geoPrimary' : 'storage.geoBackup')}</span>}
                  </span>
                )],
                [t('storage.geoState'), (
                  <span data-testid="geo-auto-state">
                    {auto?.state === 'checking' ? t('storage.geoChecking')
                      : auto?.state === 'error' ? <span className="storage-warn">{auto.error || t('storage.geoFailed')}</span>
                        : t('storage.geoIdle')}
                  </span>
                )],
                [t('storage.geoLastUpdate'), when(auto?.last_update)],
                [t('storage.geoNextCheck'), when(auto?.next_check)],
              ]}
            />
            <Button className="align-start" onClick={checkNow} loading={checking} data-testid="geo-check">{t('storage.geoCheckNow')}</Button>
          </div>
        )}

        {dbs.length === 0 ? (
          <div className="muted" data-testid="geo-none">{t('storage.geoNone')}</div>
        ) : (
          <div className="geo-dbs" data-testid="geo-dbs">
            {dbs.map((d) => (
              <div className="geo-db" key={d.kind} data-testid={`geo-db-${d.kind}`}>
                <Badge variant="blue">{t(`storage.geoKind.${d.kind}`)}</Badge>
                <span className="geo-db-type">{d.type}</span>
                <span className="muted">
                  {[d.build ? new Date(d.build).toLocaleDateString() : '', fmtBytes(d.size),
                    d.source ? (SOURCE_NAME[d.source] || t('storage.geoUploadedSrc')) : ''].filter(Boolean).join(' · ')}
                </span>
                <span className="spacer" />
                {mode === 'manual' && <Button size="sm" variant="danger" onClick={() => remove(d)}>{t('app.delete')}</Button>}
              </div>
            ))}
          </div>
        )}

        {mode === 'manual' && (
          <>
            <FileDropZone
              file={file}
              onFileSelected={setFile}
              accept=".mmdb,.gz,.tgz"
              emptyHint={t('storage.geoDrop')}
              emptyAcceptHint={t('storage.geoAccept')}
              disabled={progress !== null}
            />
            {progress !== null && <Progress value={progress} />}
            <Button variant="primary" className="align-start" onClick={upload} disabled={!file || progress !== null} data-testid="geo-upload">
              {t('storage.geoUpload')}
            </Button>
          </>
        )}
      </FormStack>
    </Card>
  );
}