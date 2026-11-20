import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Button, FormStack, FieldLabel, Hint, Table, Badge, SegmentedControl,
  useToast, useConfirm, type Column,
} from '@hy2scale/ui';
import { getUserSub, resetUserSubToken, subURL, type ProxyLink } from '@/api/subscription';
import { proxyLabel } from '@/config/proxyRegistry';
import { copyToClipboard } from '@/utils/clipboard';
import { LinkBox } from './LinkBox';

/** LAN addresses shown until all are asked for. */
const LAN_SHOWN = 2;

/** A user's subscription: its addresses and entries (its settings are in the user dialog). */
export default function UserSubscriptionTab({ userId }: { userId: string }) {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const queryClient = useQueryClient();
  const key = ['userSub', userId];
  const { data: sub } = useQuery({ queryKey: key, queryFn: () => getUserSub(userId) });
  const [busy, setBusy] = useState(false);
  const [allLAN, setAllLAN] = useState(false);
  // the format the addresses ask for: base64 links (v2ray) or a Clash proxies list
  const [fmt, setFmt] = useState<'v2ray' | 'clash'>('v2ray');

  const resetToken = async () => {
    const ok = await confirm({ title: t('sub.resetToken'), message: t('sub.resetConfirm'), confirmText: t('app.reset'), cancelText: t('app.cancel'), danger: true });
    if (!ok) return;
    setBusy(true);
    try {
      queryClient.setQueryData(key, await resetUserSubToken(userId));
      // the user object carries the token too
      queryClient.invalidateQueries({ queryKey: ['users'] });
    } catch (e: any) {
      toast.error(String(e.message || e));
      queryClient.invalidateQueries({ queryKey: key });
    } finally { setBusy(false); }
  };
  const copy = (s: string) => copyToClipboard(s).then((ok) => { if (ok) toast.success(t('app.copied')); });

  // the public address, then the LAN ones
  const lanAddrs = sub?.lan_addrs || [];
  const urls: { label: string; url: string }[] = [];
  if (sub?.enabled && sub.path) {
    const q = fmt === 'clash' ? '?format=clash' : '';
    const add = (label: string, host: string) => {
      urls.push({ label, url: subURL(sub, host) + q });
      if (sub.http) urls.push({ label: `${label} · HTTP`, url: subURL(sub, host, true) + q });
    };
    if (sub.public_host) add(t('sub.public'), sub.public_host);
    for (const ip of allLAN ? lanAddrs : lanAddrs.slice(0, LAN_SHOWN)) add(t('sub.lan'), ip);
  }

  const columns: Column<ProxyLink>[] = ([
    { key: 'name', title: t('sub.entry'), sortValue: (l) => l.name, render: (l) => <span className="sub-entry-name">{l.name}</span> },
    { key: 'proto', title: t('sub.protocol'), width: '120px', sortValue: (l) => l.proto, render: (l) => proxyLabel(l.proto) },
    {
      key: 'host', title: t('sub.host'), width: '190px', sortValue: (l) => l.host, render: (l) => (
        <span className="sub-entry-host">{l.host}{l.lan && <Badge variant="blue" className="sub-lan-badge">{t('sub.lanBadge')}</Badge>}</span>
      ),
    },
  ] as Column<ProxyLink>[]).map((c) => ({ ...c, hideable: false }));

  if (!sub) return null;
  if (!sub.enabled) return <Hint><span data-testid="sub-off">{t('sub.off')}</span></Hint>;

  return (
    <FormStack gap={16}>
      {sub.shared && <Hint>{t('sub.sharedHint')}</Hint>}
      <div className="sub-urls" data-testid="sub-urls">
        <FieldLabel>{t('sub.address')}</FieldLabel>
        <SegmentedControl<'v2ray' | 'clash'> className="align-start" value={fmt} onChange={setFmt} items={[
          { key: 'v2ray', label: t('sub.fmtV2ray'), testId: 'sub-fmt-v2ray' },
          { key: 'clash', label: 'Clash', testId: 'sub-fmt-clash' },
        ]} />
        {sub.path && urls.length === 0 && <Hint>{t('sub.noAddress')}</Hint>}
        {urls.map((u) => (
          <div key={u.url} className="sub-url-row">
            <span className="sub-url-label">{u.label}</span>
            <LinkBox url={u.url} onCopy={copy} />
          </div>
        ))}
        <div className="sub-url-actions">
          {lanAddrs.length > LAN_SHOWN && (
            <Button size="sm" variant="ghost" onClick={() => setAllLAN(!allLAN)} data-testid="sub-all-lan">
              {allLAN ? t('sub.lessLAN') : t('sub.allLAN', { n: lanAddrs.length })}
            </Button>
          )}
          <Button size="sm" onClick={resetToken} loading={busy} data-testid="sub-reset">{t('sub.resetToken')}</Button>
        </div>
      </div>

      <div data-testid="sub-entries">
        <FieldLabel>{t('sub.entries')} ({sub.entries.length})</FieldLabel>
        <Table id="users.detail.subEntries" columns={columns} data={sub.entries} rowKey={(l) => l.url}
          onRowClick={(l) => copy(l.url)} paginate={false} emptyText={t('sub.noEntries')} />
      </div>
    </FormStack>
  );
}
