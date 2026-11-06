import { useState, useCallback, type ReactNode, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import clsx from 'clsx';
import {
  Card, Button, Badge, Tooltip, TreeTable, TreeCell, MenuCheck, useToast, useConfirm, useSelection,
  type TreeNode, type TreeColumn, type TreeRowMeta,
} from '@hy2scale/ui';
import { ipSortKey } from '@/utils/ipaddr';
import * as api from '@/api';
import type { IfaceEntry, IfaceFamily, IfaceFamilyKey, IfaceAddr, Egress } from '@/api/types';
import { fmtDuration } from '@/hooks/useFormat';
import { isLinkLocal6 } from '@/utils/ipaddr';
import InterfaceModal, { childToBody } from '@/components/InterfaceModal';
import InterfaceDeleteModal from '@/components/InterfaceDeleteModal';
import InterfaceDetailModal from '@/components/InterfaceDetailModal';
import InterfaceParentModal from '@/components/InterfaceParentModal';
import ResponsiveActions from '@/components/ResponsiveActions';
import IfaceTypeBadge, { isHiddenIface } from '@/components/IfaceTypeBadge';
import InterfacesTopoView from '@/components/InterfacesTopoView';
import { usePageView, useIfaceOpts } from '@/store/pageView';
import { useNodeStore } from '@/store/node';
import { PublicIP } from '@/components/CountryFlag';

type Row = { type: 'iface'; iface: IfaceEntry };

const FAMS: IfaceFamilyKey[] = ['ipv4', 'ipv6'];
const FAM_LABEL: Record<IfaceFamilyKey, string> = { ipv4: 'IPv4', ipv6: 'IPv6' };
const FAM_SHORT: Record<IfaceFamilyKey, string> = { ipv4: 'v4', ipv6: 'v6' };

// Primary first as the backend sends it; for IPv6 a link-local entry is
// never shown as the primary when a routable one exists.
function orderedAddrs(f: IfaceFamily | undefined, fam: IfaceFamilyKey): IfaceAddr[] {
  const a = f?.addrs || [];
  if (fam === 'ipv6' && a.length > 1 && isLinkLocal6(a[0].prefix)) {
    const i = a.findIndex((x) => !isLinkLocal6(x.prefix));
    if (i > 0) return [a[i], ...a.filter((_, j) => j !== i)];
  }
  return a;
}

const hasAddr = (x: IfaceEntry) => (x.ipv4?.addrs?.length || 0) + (x.ipv6?.addrs?.length || 0) > 0;
const byName = (a: IfaceEntry, b: IfaceEntry) => a.name.localeCompare(b.name, undefined, { numeric: true });

export default function InterfacesPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const queryClient = useQueryClient();
  const [view] = usePageView('interfaces');
  const hostName = useNodeStore((s) => s.node?.name);

  const { data, isError, error } = useQuery({
    queryKey: ['ifaces'],
    queryFn: api.listInterfaces,
    refetchInterval: 2000,
  });
  const all = data?.interfaces || [];
  const showHidden = useIfaceOpts((s) => s.showHidden);
  const setShowHidden = useIfaceOpts((s) => s.setShowHidden);
  const hiddenCount = all.filter(isHiddenIface).length;
  // Visible set: NICs and sub-interfaces; the rest only on request.
  const list = showHidden ? all : all.filter((x) => !isHiddenIface(x));
  const egress: Egress = data?.egress || { ipv4: '', ipv6: '' };
  // the punch interface is shown only while this node accepts punches
  const punchIface = data?.punch_enabled ? (data.punch || '').split('/')[0] : null;

  const [detail, setDetail] = useState<string | null>(null);
  const [detailFrom, setDetailFrom] = useState<{ x: number; y: number } | undefined>();
  const [picking, setPicking] = useState(false);
  const [refreshing, setRefreshing] = useState<Set<string>>(new Set());
  const [egressBusy, setEgressBusy] = useState(false);
  const [clickPos, setClickPos] = useState<{ x: number; y: number } | undefined>();
  const [modal, setModal] = useState<{ parent: IfaceEntry | null; editing: IfaceEntry | null } | null>(null);
  const [deleting, setDeleting] = useState<IfaceEntry[]>([]);
  const selection = useSelection(list.map((x) => x.name));

  const invalidate = useCallback(() => {
    queryClient.invalidateQueries({ queryKey: ['ifaces'] });
    queryClient.invalidateQueries({ queryKey: ['interfaces'] });
  }, [queryClient]);

  const anchorOf = (e: MouseEvent) => {
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  };

  const srcLabel = (s: string) => t(`iface.src.${s}`, { defaultValue: s });
  const reasonText = (r: string) => t(`iface.reason.${r}`, { defaultValue: r });
  const addrTip = (a: IfaceAddr) => {
    const parts = [a.prefix, srcLabel(a.source)];
    if (a.expires > 0) {
      const left = Math.max(0, Math.round(a.expires - Date.now() / 1000));
      parts.push(t('iface.leaseLeft', { d: fmtDuration(left) || '0s' }));
    }
    return parts.join(' · ');
  };

  // ---- egress ----
  const putEgress = async (next: Egress) => {
    setEgressBusy(true);
    try {
      await api.setEgress(next);
      invalidate();
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setEgressBusy(false);
    }
  };
  const switchEgress = (fam: IfaceFamilyKey, name: string) => putEgress({ ...egress, [fam]: name });
  const putPunch = async (name: string) => {
    setEgressBusy(true);
    try {
      await api.setPunchInterface(name);
      invalidate();
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setEgressBusy(false);
    }
  };
  const canPunch = (x: IfaceEntry) => x.up && !!(x.ipv4?.addrs?.length || x.ipv6?.addrs?.length);
  const clearEgress = async (fam: IfaceFamilyKey) => {
    const ok = await confirm({
      title: t('iface.clearEgressTitle', { fam: FAM_LABEL[fam] }),
      message: t('iface.clearEgressMsg', { fam: FAM_LABEL[fam] }),
      danger: true,
      confirmText: t('app.confirm'),
      cancelText: t('app.cancel'),
    });
    if (ok) putEgress({ ...egress, [fam]: '' });
  };

  // Why a selected egress is unhealthy ('' = healthy).
  const egressWarning = (x: IfaceEntry, fam: IfaceFamilyKey): string => {
    const f = x[fam];
    if (!f || f.mode === 'off') return reasonText('family_off');
    if (!x.up) return reasonText('down');
    if (!f.addrs?.length) return reasonText('no_address');
    const u = x.egress_usable?.[fam];
    if (u && !u.ok) return reasonText(u.reason);
    const p = x.public?.[fam];
    if (p && p.ok === false) return p.error ? `${t('iface.publicFailed')}: ${p.error}` : t('iface.publicFailed');
    return '';
  };

  const egressBadge = (x: IfaceEntry, fam: IfaceFamilyKey) => {
    if (!data || egress[fam] !== x.name) return null;
    const warn = egressWarning(x, fam);
    return (
      <span
        className={clsx('hy-badge iface-egress', warn && 'warn')}
        title={warn || t('iface.egressOn', { fam: FAM_LABEL[fam] })}
        data-testid={`egress-${fam}`}
        data-state={warn ? 'warn' : 'on'}
      >{FAM_SHORT[fam]}</span>
    );
  };

  // Set / clear this interface as a family's egress (single selection).
  const canEgress = (x: IfaceEntry, fam: IfaceFamilyKey) => {
    const u = x.egress_usable?.[fam];
    return !!u && (u.ok || u.reason === 'no_address');
  };

  // ---- cells ----
  const addrLine = (x: IfaceEntry, fam: IfaceFamilyKey) => {
    const f = x[fam];
    const addrs = orderedAddrs(f, fam);
    const badge = egressBadge(x, fam);
    let main: ReactNode = null;
    if (!f || f.mode === 'off' || addrs.length === 0) {
      if (f && f.mode !== 'off' && f.state === 'acquiring') main = <span className="iface-pending">{t('iface.acquiring')}</span>;
      else if (f && f.mode !== 'off' && f.state === 'failed') main = <span className="iface-bad" title={f.error || undefined}>{t('iface.failed')}</span>;
      // Nothing for this family: no line, unless it is the egress.
      else if (!badge) return null;
    } else {
      main = (
        <>
          <Tooltip content={<div className="iface-tip-row">{addrTip(addrs[0])}</div>}>
            <span data-testid={`addr-${fam}`}>{addrs[0].prefix}</span>
          </Tooltip>
          {addrs.length > 1 && (
            <Tooltip content={<div>{addrs.map((a) => <div key={a.prefix} className="iface-tip-row">{addrTip(a)}</div>)}</div>}>
              <Badge variant="muted">+{addrs.length - 1}</Badge>
            </Tooltip>
          )}
        </>
      );
    }
    return (
      <div className="iface-line" data-fam={fam} key={fam}>
        <span className="iface-line-main">{main}{badge}</span>
      </div>
    );
  };
  const addrCell = (x: IfaceEntry) => {
    const lines = FAMS.map((f) => addrLine(x, f)).filter(Boolean);
    return (
      <div className="iface-lines">
        {lines.length ? lines : <div className="iface-line"><span className="iface-muted">—</span></div>}
      </div>
    );
  };

  const refreshPublic = async (name: string) => {
    setRefreshing((s) => new Set(s).add(name));
    try {
      await api.refreshPublicIP(name);
      invalidate();
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setRefreshing((s) => { const n = new Set(s); n.delete(name); return n; });
    }
  };

  const pubLine = (x: IfaceEntry, fam: IfaceFamilyKey) => {
    const p = x.public?.[fam];
    let c: ReactNode;
    if (!p) return null;
    if (!p.ok) c = <span className="iface-bad" title={p.error || undefined} data-testid={`pub-${fam}-bad`}>{t('iface.unavailable')}</span>;
    else c = (
      <>
        <PublicIP className="iface-pub-ip" testId={`pub-${fam}`} ip={p.ip}
          title={p.checked ? t('iface.checkedAt', { time: new Date(p.checked * 1000).toLocaleString() }) : undefined} />
        {p.direct && <span className="iface-tag faint" data-testid="tag-direct">{t('iface.direct')}</span>}
      </>
    );
    return <div className="iface-line" data-fam={fam} key={fam}>{c}</div>;
  };

  const pubCell = (x: IfaceEntry) => {
    const detectable = !!(x.public?.ipv4 || x.public?.ipv6);
    const busy = refreshing.has(x.name);
    return (
      <div
        className={clsx('iface-lines', detectable && 'iface-pub', busy && 'busy')}
        role={detectable ? 'button' : undefined}
        title={detectable ? t('iface.refreshPublic') : undefined}
        data-testid="pub-cell"
        onClick={detectable ? (e) => { e.stopPropagation(); if (!busy) refreshPublic(x.name); } : undefined}
      >
        {detectable
          ? FAMS.map((f) => pubLine(x, f))
          : <div className="iface-line"><span className="iface-muted">—</span></div>}
      </div>
    );
  };

  const openEdit = (x: IfaceEntry, e?: MouseEvent) => {
    if (e) setClickPos(anchorOf(e));
    setModal({ parent: list.find((p) => p.name === x.parent) || null, editing: x });
  };

  const nameCell = (r: Row, meta: TreeRowMeta) => {
    const x = r.iface;
    const child = x.kind === 'child';
    return (
      <TreeCell meta={meta}>
        <span className="iface-name-wrap">
          {child
            ? <button type="button" className="iface-name iface-name-link" data-testid="child-edit"
                onClick={(e) => openEdit(x, e)}>{x.name}</button>
            : <span className="iface-name">{x.name}</span>}
          <IfaceTypeBadge iface={x} />
          {x.role && <span className="iface-tag" data-testid="tag-role">{t(`iface.role.${x.role}`)}</span>}
          {punchIface === x.name && <span className="iface-tag punch" data-testid="tag-punch">{t('iface.punchTag')}</span>}
          {child && <span className="iface-tag" data-testid="tag-mode">{t(x.mode === 'system' ? 'iface.modeSystem' : 'iface.modeBuiltin')}</span>}
          {child && x.vlan?.enabled && <span className="iface-tag" data-testid="tag-vlan">{t('iface.vlanTag', { id: x.vlan.id })}</span>}
          {child && x.enabled === false && <span className="iface-tag off">{t('iface.disabledTag')}</span>}
          {!x.up && x.enabled !== false && <span className="iface-tag off">{t('iface.down')}</span>}
          {x.wireless && <span className="iface-tag faint">{t('iface.wireless')}</span>}
        </span>
      </TreeCell>
    );
  };

  // ---- actions ----
  const renew = async (x: IfaceEntry) => {
    try {
      await api.renewIface(x.name);
      toast.success(t('iface.renewSent', { name: x.name }));
      invalidate();
    } catch (e: any) { toast.error(String(e.message || e)); }
  };
  const setEnabled = async (xs: IfaceEntry[], enabled: boolean) => {
    try {
      for (const x of xs) {
        if ((x.enabled !== false) === enabled) continue;
        const body = childToBody(x);
        body.enabled = enabled;
        await api.updateChildIface(x.name, body);
      }
    } catch (e: any) { toast.error(String(e.message || e)); }
    invalidate();
  };
  const toggleEnabled = (x: IfaceEntry) => setEnabled([x], x.enabled === false);
  const selectedItems = list.filter((x) => selection.selected.has(x.name));

  // ---- tree ----
  const buildNodes = (): TreeNode<Row>[] => {
    const children = list.filter((x) => x.kind === 'child');
    const others = list.filter((x) => x.kind !== 'child');
    const tops = [...others];
    const loopback = (x: IfaceEntry) => x.name === 'lo' || !!x.ipv4?.addrs?.some((a) => a.prefix.startsWith('127.'));
    // NICs, then other links (addressed first), container networks last.
    const rank = (x: IfaceEntry) => (x.kind === 'physical' ? 0 : x.container ? 4 : loopback(x) ? 3 : hasAddr(x) ? 1 : 2);
    tops.sort((a, b) => rank(a) - rank(b) || byName(a, b));
    const known = new Set(others.map((x) => x.name));

    const node = (x: IfaceEntry): TreeNode<Row> => {
      const kids = children.filter((c) => c.parent === x.name).sort(byName);
      return {
        key: x.name,
        data: { type: 'iface', iface: x },
        expanded: true,
        className: clsx('iface-row', x.kind === 'child' && 'sub-row', x.enabled === false && 'disabled-row'),
        children: x.kind !== 'child' && kids.length ? kids.map(node) : undefined,
      };
    };
    const out = tops.map(node);
    // A child whose parent NIC is gone still gets its own row.
    for (const c of children.filter((c) => !known.has(c.parent)).sort(byName)) out.push(node(c));
    return out;
  };

  const columns: TreeColumn<Row>[] = [
    { key: 'name', title: t('iface.name'), className: 'col-name', render: nameCell,
      sortValue: (r) => (r.type === 'iface' ? r.iface.name : null) },
    {
      key: 'addr', title: t('iface.address'), className: 'col-iface-addr',
      render: (r) => r.type === 'iface'
        ? addrCell(r.iface)
        : null,
      sortValue: (r) => (r.type === 'iface' ? ipSortKey(orderedAddrs(r.iface.ipv4, 'ipv4')[0]?.prefix || orderedAddrs(r.iface.ipv6, 'ipv6')[0]?.prefix) : null),
    },
    { key: 'pub', title: t('iface.publicIp'), className: 'col-iface-pub', render: (r) => (r.type === 'iface' ? pubCell(r.iface) : null),
      sortValue: (r) => (r.type === 'iface' ? ipSortKey(r.iface.public?.ipv4?.ip || r.iface.public?.ipv6?.ip) : null) },
  ];

  const cap = data?.capability;
  const capReason = cap && !cap.children ? (cap.reason ? t(`iface.cap.${cap.reason}`, { defaultValue: cap.reason }) : t('iface.cap.unavailable')) : '';
  const parents = list.filter((x) => x.kind === 'physical' && x.can_have_children).sort(byName);
  const detailIface = detail ? list.find((x) => x.name === detail) || null : null;
  // Selection-driven actions. Bulk actions only apply to a selection of one
  // kind; a single selection also gets its own shortcuts.
  const selKinds = new Set(selectedItems.map((x) => x.kind));
  const selKind = selKinds.size === 1 ? selectedItems[0].kind : null;
  const selParent = selectedItems.length === 1 && selKind === 'physical' && selectedItems[0].can_have_children && !capReason
    ? selectedItems[0] : null;
  const selActions = () => {
    if (!selKind) return null;
    const one = selectedItems.length === 1 ? selectedItems[0] : null;
    const pubTargets = selectedItems.filter((x) => x.public?.ipv4 || x.public?.ipv6);
    const refreshBtn = pubTargets.length > 0 && (
      <Button size="sm" data-testid="bulk-refresh-public"
        onClick={() => pubTargets.forEach((x) => { if (!refreshing.has(x.name)) refreshPublic(x.name); })}>
        {t('iface.refreshPublic')}
      </Button>
    );
    const egressBtns = one && FAMS.map((fam) => (
      egress[fam] === one.name
        ? <Button key={fam} size="sm" disabled={egressBusy} data-testid={`egress-clear-${fam}`}
            onClick={() => clearEgress(fam)}>{t('iface.clearEgressTitle', { fam: FAM_LABEL[fam] })}</Button>
        : canEgress(one, fam)
          ? <Button key={fam} size="sm" disabled={egressBusy} data-testid={`egress-set-${fam}`}
              onClick={() => switchEgress(fam, one.name)}>{t('iface.egressSet', { fam: FAM_LABEL[fam] })}</Button>
          : null
    ));
    const punchBtn = one && punchIface !== null && (
      punchIface === one.name
        ? <Button size="sm" disabled={egressBusy} data-testid="punch-clear" onClick={() => putPunch('')}>{t('iface.punchClear')}</Button>
        : canPunch(one)
          ? <Button size="sm" disabled={egressBusy} data-testid="punch-set" onClick={() => putPunch(one.name)}>{t('iface.punchSet')}</Button>
          : null
    );
    if (selKind !== 'child') return <>{egressBtns}{punchBtn}{refreshBtn}</>;
    const hasDisabled = selectedItems.some((x) => x.enabled === false);
    const hasEnabled = selectedItems.some((x) => x.enabled !== false);
    const dhcp = selectedItems.filter((x) => x.enabled !== false && x.ipv4?.mode === 'dhcp');
    return <>
      {hasDisabled && <Button size="sm" onClick={() => setEnabled(selectedItems, true)} data-testid="bulk-enable">{t('app.bulkEnable')}</Button>}
      {hasEnabled && <Button size="sm" onClick={() => setEnabled(selectedItems, false)} data-testid="bulk-disable">{t('app.bulkDisable')}</Button>}
      {dhcp.length > 0 && <Button size="sm" onClick={() => dhcp.forEach((x) => renew(x))} data-testid="bulk-renew">{t('iface.renew')}</Button>}
      {egressBtns}
      {punchBtn}
      {refreshBtn}
      <Button size="sm" variant="danger" data-testid="bulk-delete"
        onClick={(e) => { setClickPos(anchorOf(e)); setDeleting(selectedItems); }}>{t('app.bulkDelete')}</Button>
      {one && (
        <Button size="sm" variant="success" data-testid="edit-selected-btn"
          onClick={(e) => openEdit(one, e)}>{t('app.edit')}</Button>
      )}
    </>;
  };

  const onRowClick = (node: TreeNode<Row>, _meta: unknown, origin: { x: number; y: number }) => {
    setDetailFrom(origin);
    setDetail(node.data.iface.name);
  };

  const nodes = buildNodes();
  const otherNames = (exclude: string) => list
    .filter((x) => x.name !== exclude && !x.container)
    .sort(byName)
    .map((x) => x.name);

  // Shared by the list (card header) and the topology (top-right overlay).
  const actionBar = (
    <ResponsiveActions
      selectedCount={selection.count}
      onClearSelection={selection.clear}
      // In the topology this lives in the style menu.
      more={view === 'topo' ? undefined : (
        <MenuCheck checked={showHidden} onChange={setShowHidden} testId="show-hidden"
          count={hiddenCount > 0 ? hiddenCount : undefined}>
          {t('iface.showHidden')}
        </MenuCheck>
      )}
    >
      {selActions()}
      <span title={capReason || undefined} className="inline-flex" data-testid="add-child-wrap">
        <Button size="sm" variant="primary" disabled={!!capReason || parents.length === 0} data-testid="add-child"
          onClick={(e) => {
            setClickPos(anchorOf(e));
            // One selected NIC that takes children: skip the picker.
            if (selParent) setModal({ parent: selParent, editing: null });
            else setPicking(true);
          }}>
          {t('iface.addChild')}
        </Button>
      </span>
    </ResponsiveActions>
  );
  const emptyText = <span>{isError ? `${t('iface.loadFailed')}: ${String((error as any)?.message || error)}` : data ? t('iface.empty') : t('app.loading')}</span>;

  const modals = (
    <>
      <InterfaceParentModal
        open={picking}
        onClose={() => setPicking(false)}
        candidates={parents}
        animateFrom={clickPos}
        onNext={(parent) => { setPicking(false); setModal({ parent, editing: null }); }}
      />

      <InterfaceDetailModal
        open={!!detailIface}
        onClose={() => setDetail(null)}
        iface={detailIface}
        parent={detailIface?.kind === 'child' ? list.find((p) => p.name === detailIface.parent) : undefined}
        animateFrom={detailFrom}
        onEdit={() => { const x = detailIface!; setDetail(null); openEdit(x); }}
        onToggle={() => toggleEnabled(detailIface!)}
        onRenew={() => renew(detailIface!)}
        onDelete={() => { const x = detailIface!; setDetail(null); setDeleting([x]); }}
        onAddChild={detailIface?.kind === 'physical' && detailIface.can_have_children
          ? () => { const x = detailIface!; setDetail(null); setModal({ parent: x, editing: null }); }
          : undefined}
        addChildDisabledReason={capReason}
      />

      <InterfaceModal
        open={!!modal}
        onClose={() => setModal(null)}
        parent={modal?.parent ?? null}
        editing={modal?.editing ?? null}
        parentName={modal?.editing?.parent ?? modal?.parent?.name ?? ''}
        parents={parents}
        existingNames={list.map((x) => x.name)}
        animateFrom={clickPos}
      />

      <InterfaceDeleteModal
        open={deleting.length > 0}
        onClose={() => setDeleting([])}
        targets={deleting}
        candidates={deleting.length ? otherNames(deleting[0].name) : []}
        animateFrom={clickPos}
        onDeleted={selection.clear}
      />
    </>
  );

  if (view === 'topo') {
    return (
      <div className="hy-topo-full">
        <InterfacesTopoView
          list={list}
          egress={egress}
          punchIface={punchIface}
          loaded={!!data || isError}
          emptyText={emptyText}
          selection={selection}
          actions={actionBar}
          hostName={hostName}
          egressWarning={egressWarning}
          onOpenDetail={(name, anchor) => { setDetailFrom(anchor); setDetail(name); }}
          showHidden={showHidden}
          onShowHidden={setShowHidden}
          hiddenCount={hiddenCount}
        />
        {modals}
      </div>
    );
  }

  return (
    <div className="hy-page">
      <Card
        title={t('iface.title')}
        count={list.length}
        noPadding
        actions={actionBar}
      >
        <TreeTable
          id="interfaces"
          columns={columns}
          nodes={nodes}
          paginate={false}
          selection={selection}
          isSelectable={(node) => node.data.type === 'iface'}
          onRowClick={onRowClick}
          emptyText={emptyText}
        />
      </Card>
      {modals}
    </div>
  );
}
