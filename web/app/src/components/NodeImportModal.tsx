import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { Modal, Button, Textarea, FormGroup, FormStack, Hint, KeyValueList, KeyValue, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import type { ClientEntry } from '@/api';
import { useNodeStore } from '@/store/node';
import { joinAddr, parseNodeLink, punchSignalPaths, type ParsedLink } from '@/utils/nodeLink';

interface Props {
  open: boolean;
  onClose: () => void;
  animateFrom?: { x: number; y: number };
}

/** What an import creates, and how the punch would be signalled. */
interface Plan {
  kind: ParsedLink['kind'];
  entry: ClientEntry;
  /** the link offers punching but no signalling path is reachable from here */
  punchUnreachable: boolean;
  ignored: string[];
}

// NodeImportModal: paste a node link — a hy2scale:// link (all public
// addresses, punch signalling through nodes both sides reach) or a
// hysteria2:// / hy2:// link — and add it as an outbound node. The kind is
// detected from the text. Reached via the "Add Node" button's menu.
export default function NodeImportModal({ open, onClose, animateFrom }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();
  const exitPaths = useNodeStore((s) => s.exitPaths);
  const selfId = useNodeStore((s) => s.node?.node_id);

  const [url, setUrl] = useState('');
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!open) {
      setUrl('');
      setLoading(false);
    }
  }, [open]);

  const plan = useMemo<Plan | null>(() => {
    const p = parseNodeLink(url);
    if (!p) return null;
    if (p.kind === 'hy2') {
      const h = p.hy2;
      const addr = joinAddr(h.host, h.port);
      return {
        kind: 'hy2', punchUnreachable: false, ignored: h.unsupportedNotes,
        entry: {
          name: h.name || addr, addr, password: h.password,
          sni: h.sni || undefined, insecure: h.insecure || undefined,
          max_tx: h.upMbps ? Math.round(h.upMbps * 125000) : undefined,
          max_rx: h.downMbps ? Math.round(h.downMbps * 125000) : undefined,
          fast_open: h.fastOpen || undefined,
        } as ClientEntry,
      };
    }
    const l = p.link;
    const signal = l.punch ? punchSignalPaths(l.name, l.punch.via, exitPaths, selfId) : [];
    const entry = {
      name: l.name, addr: l.addrs[0] || '', password: l.password,
      sni: l.sni || undefined, insecure: l.insecure || undefined,
    } as ClientEntry;
    if (l.addrs.length > 1) entry.addrs = l.addrs;
    if (signal.length) entry.punch = { signal, reflectors: [] } as ClientEntry['punch'];
    return { kind: 'hy2scale', entry, punchUnreachable: !!l.punch && !signal.length, ignored: [] };
  }, [url, exitPaths, selfId]);

  const usable = !!plan && (!!plan.entry.addr || !!plan.entry.punch);

  const handleImport = async () => {
    if (!plan || !usable) {
      toast.error(t('nodes.importParseFail'));
      return;
    }
    if (!plan.entry.password) {
      toast.error(t('nodes.passRequired'));
      return;
    }
    setLoading(true);
    try {
      await api.createClient(plan.entry);
      toast.success(t('nodes.saved', { name: plan.entry.name }));
      queryClient.invalidateQueries({ queryKey: ['topology'] });
      onClose();
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  const e = plan?.entry;
  const addrs = e ? (e.addrs?.length ? e.addrs : e.addr ? [e.addr] : []) : [];
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('nodes.importTitle')}
      animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          <Button variant="primary" onClick={handleImport} loading={loading} disabled={!usable} data-testid="node-import-btn">
            {t('nodes.importBtn')}
          </Button>
        </>
      }
    >
      <FormStack>
        <FormGroup label={t('nodes.importHint')} required>
          <Textarea
            value={url}
            onChange={(ev) => setUrl(ev.target.value)}
            placeholder={t('nodes.importPlaceholder')}
            rows={4}
            monospace
            data-testid="hy2-import-url"
          />
        </FormGroup>

        {url.trim() && !plan && (
          <div className="hy-warn-banner">{t('nodes.importParseFail')}</div>
        )}

        {plan && e && (
          <FormGroup label={t('nodes.importPreview')}>
            <KeyValueList variant="compact" className="import-preview" testId="hy2-import-preview">
              <Row label={t('nodes.importKind')} value={plan.kind === 'hy2scale' ? 'hy2scale' : 'Hysteria 2'} />
              <Row label={t('nodes.importLabel')} value={e.name} />
              <Row label={t('nodes.address')} value={addrs.join(', ') || '—'} mono />
              <Row label={t('nodes.password')} value={e.password ? mask(e.password) : '—'} mono />
              {e.sni && <Row label={t('nodes.sni')} value={e.sni} mono />}
              <Row label={t('nodes.skipVerify')} value={e.insecure ? '✓' : '—'} />
              {e.max_tx !== undefined && <Row label={t('nodes.upload')} value={`${Math.round(e.max_tx / 125000)} Mbps`} />}
              {e.max_rx !== undefined && <Row label={t('nodes.download')} value={`${Math.round(e.max_rx / 125000)} Mbps`} />}
              {e.fast_open && <Row label={t('nodes.fastOpen')} value="✓" />}
              {e.punch && <Row label={t('nodes.addrPunch')} value={e.punch.signal.join(', ')} mono />}
            </KeyValueList>
            {plan.punchUnreachable && <div data-testid="import-punch-unreachable"><Hint className="hint-gap">{t('nodes.importPunchUnreachable')}</Hint></div>}
            {plan.ignored.length > 0 && <Hint className="hint-gap">{t('nodes.importIgnored', { fields: plan.ignored.join(', ') })}</Hint>}
          </FormGroup>
        )}
      </FormStack>
    </Modal>
  );
}

function Row({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return <KeyValue label={label} mono={mono}>{value}</KeyValue>;
}

function mask(s: string): string {
  if (s.length <= 4) return '••••';
  return s.slice(0, 2) + '•'.repeat(Math.max(4, s.length - 4)) + s.slice(-2);
}
