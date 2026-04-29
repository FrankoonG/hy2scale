import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { Modal, Button, Textarea, FormGroup, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import type { ClientEntry } from '@/api';
import { parseHy2Url, type ParsedHy2 } from '@/utils/parseHy2Url';

interface Props {
  open: boolean;
  onClose: () => void;
  animateFrom?: { x: number; y: number };
}

// NodeImportModal lets the user paste a hysteria2:// (or hy2://) URI and
// import it as an outbound client without filling out the full form by
// hand. Reached via long-press on the "Add Node" button. Live-parses on
// every keystroke so the preview reflects what would be imported.
export default function NodeImportModal({ open, onClose, animateFrom }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();

  const [url, setUrl] = useState('');
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!open) {
      setUrl('');
      setLoading(false);
    }
  }, [open]);

  const parsed = useMemo<ParsedHy2 | null>(() => parseHy2Url(url), [url]);

  const buildEntry = (p: ParsedHy2): ClientEntry => {
    const addr = p.host.includes(':') && !p.host.startsWith('[')
      ? `[${p.host}]:${p.port}`
      : `${p.host}:${p.port}`;
    return {
      name: p.name || addr,
      addr,
      password: p.password,
      sni: p.sni || undefined,
      insecure: p.insecure || undefined,
      max_tx: p.upMbps ? Math.round(p.upMbps * 125000) : undefined,
      max_rx: p.downMbps ? Math.round(p.downMbps * 125000) : undefined,
      fast_open: p.fastOpen || undefined,
    } as ClientEntry;
  };

  const handleImport = async () => {
    if (!parsed) {
      toast.error(t('nodes.importParseFail'));
      return;
    }
    if (!parsed.password) {
      toast.error(t('nodes.passRequired'));
      return;
    }
    setLoading(true);
    try {
      await api.createClient(buildEntry(parsed));
      toast.success(t('nodes.saved', { name: parsed.name || `${parsed.host}:${parsed.port}` }));
      queryClient.invalidateQueries({ queryKey: ['topology'] });
      onClose();
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('nodes.importTitle')}
      animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          <Button variant="primary" onClick={handleImport} loading={loading} disabled={!parsed}>
            {t('nodes.importBtn')}
          </Button>
        </>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <FormGroup label={t('nodes.importHint')} required>
          <Textarea
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder={t('nodes.importPlaceholder')}
            rows={4}
            monospace
            data-testid="hy2-import-url"
          />
        </FormGroup>

        {url.trim() && !parsed && (
          <div className="hy-warn-banner" style={{ marginTop: 0 }}>{t('nodes.importParseFail')}</div>
        )}

        {parsed && (
          <FormGroup label={t('nodes.importPreview')}>
            <div style={{ background: 'var(--bg-secondary)', borderRadius: 6, padding: '6px 12px' }} data-testid="hy2-import-preview">
              <Row label={t('nodes.address')} value={`${parsed.host}:${parsed.port}`} mono />
              <Row label={t('nodes.password')} value={parsed.password ? mask(parsed.password) : '—'} mono />
              {parsed.name && <Row label={t('nodes.importLabel')} value={parsed.name} />}
              {parsed.sni && <Row label={t('nodes.sni')} value={parsed.sni} mono />}
              {parsed.insecure !== undefined && <Row label={t('nodes.skipVerify')} value={parsed.insecure ? '✓' : '—'} />}
              {parsed.upMbps !== undefined && <Row label={t('nodes.upload')} value={`${parsed.upMbps} Mbps`} />}
              {parsed.downMbps !== undefined && <Row label={t('nodes.download')} value={`${parsed.downMbps} Mbps`} />}
              {parsed.fastOpen && <Row label={t('nodes.fastOpen')} value="✓" />}
              {parsed.unsupportedNotes.length > 0 && (
                <div style={{ marginTop: 8, color: 'var(--text-muted)', fontSize: 12 }}>
                  {t('nodes.importIgnored', { fields: parsed.unsupportedNotes.join(', ') })}
                </div>
              )}
            </div>
          </FormGroup>
        )}
      </div>
    </Modal>
  );
}

function Row({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div style={{ display: 'flex', justifyContent: 'space-between', gap: 12, padding: '4px 0', borderBottom: '1px solid var(--border)', fontSize: 13 }}>
      <span style={{ color: 'var(--text-muted)' }}>{label}</span>
      <span style={{ fontFamily: mono ? 'var(--mono)' : undefined, wordBreak: 'break-all', textAlign: 'right' }}>{value}</span>
    </div>
  );
}

function mask(s: string): string {
  if (s.length <= 4) return '••••';
  return s.slice(0, 2) + '•'.repeat(Math.max(4, s.length - 4)) + s.slice(-2);
}
