import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { Modal, Button, Input, Toggle, Select, FormGroup, FormGrid, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import type { IfaceEntry, ChildIfaceBody } from '@/api/types';
import { useDialogInit } from '@/hooks/useFormInit';
import { OrderedList, toListItems, fromListItems, type ListItem } from './OrderedList';
import {
  isIPv4, isIPv6, parseCIDR4, parseCIDR6, inSubnet4, isNetOrBroadcast4,
  isUnicastMAC, randomMAC, isHostname,
} from '@/utils/ipaddr';

type V4Mode = ChildIfaceBody['ipv4']['mode'];
type V6Mode = ChildIfaceBody['ipv6']['mode'];

/** Current settings of a child interface as a PUT body. */
export function childToBody(x: IfaceEntry): ChildIfaceBody {
  const v4: V4Mode = x.ipv4?.mode === 'dhcp' || x.ipv4?.mode === 'static' ? x.ipv4.mode : 'off';
  const v6: V6Mode = x.ipv6?.mode === 'slaac' || x.ipv6?.mode === 'static' ? x.ipv6.mode : 'off';
  return {
    name: x.name,
    parent: x.parent,
    mode: x.mode === 'system' ? 'system' : 'builtin',
    mac: x.mac,
    vlan: { enabled: !!x.vlan?.enabled, id: x.vlan?.id || 0 },
    mtu: x.mtu || 0,
    enabled: x.enabled !== false,
    ipv4: {
      mode: v4,
      addrs: v4 === 'static' ? (x.ipv4.addrs || []).map((a) => a.prefix) : [],
      gateway: v4 === 'static' ? x.ipv4.gateway || '' : '',
      dns: v4 === 'static' ? x.ipv4.dns || [] : [],
      hostname: x.ipv4?.hostname || '',
      client_id: x.ipv4?.client_id || '',
    },
    ipv6: {
      mode: v6,
      addrs: v6 === 'static' ? (x.ipv6.addrs || []).map((a) => a.prefix) : [],
      gateway: v6 === 'static' ? x.ipv6.gateway || '' : '',
      dns: v6 === 'static' ? x.ipv6.dns || [] : [],
    },
  };
}

interface Props {
  open: boolean;
  onClose: () => void;
  /** Parent NIC (create) — also used for its MTU / wireless flag. */
  parent: IfaceEntry | null;
  /** Child being edited; null = create under `parent`. */
  editing: IfaceEntry | null;
  parentName: string;
  existingNames: string[];
  animateFrom?: { x: number; y: number };
}

const NAME_RE = /^[A-Za-z0-9-]{1,15}$/;
const CLIENT_ID_RE = /^[\x21-\x7e]{1,128}$/;

export default function InterfaceModal({ open, onClose, parent, editing, parentName, existingNames, animateFrom }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();

  const [name, setName] = useState('');
  const [mode, setMode] = useState<'builtin' | 'system'>('builtin');
  const [mac, setMac] = useState('');
  const [vlanOn, setVlanOn] = useState(false);
  const [vlanId, setVlanId] = useState('');
  const [mtu, setMtu] = useState('');
  const [enabled, setEnabled] = useState(true);
  const [v4Mode, setV4Mode] = useState<V4Mode>('dhcp');
  const [v4Addrs, setV4Addrs] = useState<ListItem[]>(toListItems([]));
  const [v4Gw, setV4Gw] = useState('');
  const [v4Dns, setV4Dns] = useState<ListItem[]>(toListItems([]));
  const [hostname, setHostname] = useState('');
  const [clientId, setClientId] = useState('');
  const [v6Mode, setV6Mode] = useState<V6Mode>('off');
  const [v6Addrs, setV6Addrs] = useState<ListItem[]>(toListItems([]));
  const [v6Gw, setV6Gw] = useState('');
  const [v6Dns, setV6Dns] = useState<ListItem[]>(toListItems([]));
  const [submitted, setSubmitted] = useState(false);
  const [saving, setSaving] = useState(false);

  useDialogInit(open, editing?.name ?? `new:${parentName}`, () => {
    setSubmitted(false);
    if (!editing) {
      setName(''); setMode('builtin'); setMac(randomMAC());
      setVlanOn(false); setVlanId(''); setMtu(''); setEnabled(true);
      setV4Mode('dhcp'); setV4Addrs(toListItems([])); setV4Gw(''); setV4Dns(toListItems([]));
      setHostname(''); setClientId('');
      setV6Mode('off'); setV6Addrs(toListItems([])); setV6Gw(''); setV6Dns(toListItems([]));
      return;
    }
    const b = childToBody(editing);
    setName(b.name); setMode(b.mode); setMac(b.mac);
    setVlanOn(b.vlan.enabled); setVlanId(b.vlan.id ? String(b.vlan.id) : '');
    setMtu(b.mtu ? String(b.mtu) : ''); setEnabled(b.enabled);
    setV4Mode(b.ipv4.mode); setV4Addrs(toListItems(b.ipv4.addrs)); setV4Gw(b.ipv4.gateway);
    setV4Dns(toListItems(b.ipv4.dns)); setHostname(b.ipv4.hostname); setClientId(b.ipv4.client_id);
    setV6Mode(b.ipv6.mode); setV6Addrs(toListItems(b.ipv6.addrs)); setV6Gw(b.ipv6.gateway);
    setV6Dns(toListItems(b.ipv6.dns));
  });

  const parentMtu = parent?.mtu || 0;

  const validate = (): Record<string, string> => {
    const e: Record<string, string> = {};
    if (!NAME_RE.test(name)) e.name = t('iface.err.name');
    else if (!editing && existingNames.includes(name)) e.name = t('iface.err.nameTaken');
    if (!isUnicastMAC(mac.trim())) e.mac = t('iface.err.mac');
    if (vlanOn) {
      const id = Number(vlanId);
      if (!/^\d+$/.test(vlanId) || id < 1 || id > 4094) e.vlan = t('iface.err.vlan');
    }
    if (mtu.trim()) {
      const m = Number(mtu);
      if (!/^\d+$/.test(mtu.trim()) || m < 68 || m > 9216) e.mtu = t('iface.err.mtu');
      else if (parentMtu && m > parentMtu) e.mtu = t('iface.err.mtuParent', { mtu: parentMtu });
      else if (v6Mode !== 'off' && m < 1280) e.mtu = t('iface.err.mtu6');
    }
    if (v4Mode === 'dhcp') {
      if (hostname.trim() && !isHostname(hostname.trim())) e.hostname = t('iface.err.hostname');
      if (clientId.trim() && !CLIENT_ID_RE.test(clientId.trim())) e.clientId = t('iface.err.clientId');
    }
    if (v4Mode === 'static') {
      const addrs = fromListItems(v4Addrs);
      if (!addrs.length) e.v4Addrs = t('iface.err.addrRequired');
      const parsed = [];
      for (const a of addrs) {
        const p = parseCIDR4(a);
        if (!p || isNetOrBroadcast4(p)) { e.v4Addrs = t('iface.err.addr4', { v: a }); break; }
        parsed.push(p);
      }
      if (!e.v4Addrs && new Set(parsed.map((p) => p.ip)).size !== parsed.length) e.v4Addrs = t('iface.err.addrDup');
      const gw = v4Gw.trim();
      if (gw) {
        if (!isIPv4(gw)) e.v4Gw = t('iface.err.gw4');
        else if (parsed.some((p) => p.ip === gw)) e.v4Gw = t('iface.err.gwSelf');
        else if (parsed.length && !parsed.some((p) => p.bits >= 31 || inSubnet4(gw, p))) e.v4Gw = t('iface.err.gwSubnet');
      }
      const bad = fromListItems(v4Dns).find((d) => !isIPv4(d));
      if (bad !== undefined) e.v4Dns = t('iface.err.dns4', { v: bad });
    }
    if (v6Mode === 'static') {
      const addrs = fromListItems(v6Addrs);
      if (!addrs.length) e.v6Addrs = t('iface.err.addrRequired');
      const bad = addrs.find((a) => !parseCIDR6(a));
      if (bad !== undefined) e.v6Addrs = t('iface.err.addr6', { v: bad });
      else if (new Set(addrs.map((a) => parseCIDR6(a)!.ip.toLowerCase())).size !== addrs.length) e.v6Addrs = t('iface.err.addrDup');
      const gw = v6Gw.trim();
      if (gw && !isIPv6(gw)) e.v6Gw = t('iface.err.gw6');
      const badDns = fromListItems(v6Dns).find((d) => !isIPv6(d));
      if (badDns !== undefined) e.v6Dns = t('iface.err.dns6', { v: badDns });
    }
    return e;
  };

  const errors = submitted ? validate() : {};

  const handleSubmit = async () => {
    setSubmitted(true);
    if (Object.keys(validate()).length) return;
    const body: ChildIfaceBody = {
      name: name.trim(),
      parent: parentName,
      mode,
      mac: mac.trim().toLowerCase(),
      vlan: { enabled: vlanOn, id: vlanOn ? Number(vlanId) : 0 },
      mtu: mtu.trim() ? Number(mtu) : 0,
      enabled,
      ipv4: {
        mode: v4Mode,
        addrs: v4Mode === 'static' ? fromListItems(v4Addrs) : [],
        gateway: v4Mode === 'static' ? v4Gw.trim() : '',
        dns: v4Mode === 'static' ? fromListItems(v4Dns) : [],
        hostname: v4Mode === 'dhcp' ? hostname.trim() : '',
        client_id: v4Mode === 'dhcp' ? clientId.trim() : '',
      },
      ipv6: {
        mode: v6Mode,
        addrs: v6Mode === 'static' ? fromListItems(v6Addrs) : [],
        gateway: v6Mode === 'static' ? v6Gw.trim() : '',
        dns: v6Mode === 'static' ? fromListItems(v6Dns) : [],
      },
    };
    setSaving(true);
    try {
      if (editing) await api.updateChildIface(editing.name, body);
      else await api.createChildIface(body);
      toast.success(t('iface.saved', { name: body.name }));
      queryClient.invalidateQueries({ queryKey: ['ifaces'] });
      queryClient.invalidateQueries({ queryKey: ['interfaces'] });
      onClose();
    } catch (err: any) {
      toast.error(String(err.message || err));
    } finally {
      setSaving(false);
    }
  };

  const wireless = !editing && parent?.wireless;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={editing ? t('iface.editChildTitle', { name: editing.name }) : t('iface.addChildTitle')}
      animateFrom={animateFrom}
      wide
      footer={
        <>
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          <Button variant="primary" onClick={handleSubmit} loading={saving} data-testid="iface-save">{t('app.save')}</Button>
        </>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }} data-testid="iface-modal">
        {wireless && <div className="hy-alert-warn">{t('iface.wirelessHint')}</div>}
        <FormGrid>
          <FormGroup label={t('iface.name')} required error={errors.name}>
            <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={15} disabled={!!editing}
              placeholder="wan-b" data-testid="iface-name" />
          </FormGroup>
          <FormGroup label={t('iface.parent')}>
            <Input value={parentName} disabled />
          </FormGroup>
          <FormGroup label={t('iface.mode')}>
            <Select
              value={mode}
              onChange={(e) => setMode(e.target.value === 'system' ? 'system' : 'builtin')}
              options={[
                { value: 'builtin', label: t('iface.modeBuiltin') },
                { value: 'system', label: t('iface.modeSystem'), disabled: true },
              ]}
            />
            <div className="iface-hint">{t('iface.systemModeSoon')}</div>
          </FormGroup>
          <FormGroup label={t('iface.enabled')}>
            <div style={{ paddingTop: 6 }}>
              <Toggle checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
            </div>
          </FormGroup>
        </FormGrid>

        <FormGroup label={t('iface.mac')} required error={errors.mac}>
          <div style={{ display: 'flex', gap: 8 }}>
            <Input value={mac} onChange={(e) => setMac(e.target.value)} placeholder="02:00:00:00:00:01"
              style={{ flex: 1, fontFamily: 'var(--mono)' }} data-testid="iface-mac" />
            <Button onClick={() => setMac(randomMAC())} data-testid="iface-mac-regen">{t('iface.regenerate')}</Button>
          </div>
        </FormGroup>

        <FormGrid>
          <FormGroup label={t('iface.vlan')} error={errors.vlan}>
            <div style={{ display: 'flex', gap: 10, alignItems: 'center' }}>
              <Toggle checked={vlanOn} onChange={(e) => setVlanOn(e.target.checked)} data-testid="iface-vlan-toggle" />
              {vlanOn && (
                <Input type="number" min={1} max={4094} value={vlanId} onChange={(e) => setVlanId(e.target.value)}
                  placeholder={t('iface.vlanId')} style={{ flex: 1 }} data-testid="iface-vlan-id" />
              )}
            </div>
          </FormGroup>
          <FormGroup label={t('iface.mtu')} error={errors.mtu}>
            <Input type="number" value={mtu} onChange={(e) => setMtu(e.target.value)}
              placeholder={parentMtu ? `${t('iface.mtuFollow')} (${parentMtu})` : t('iface.mtuFollow')} data-testid="iface-mtu" />
          </FormGroup>
        </FormGrid>

        {/* IPv4 */}
        <div className="iface-form-section">IPv4</div>
        <FormGroup label={t('iface.addrMode')}>
          <Select
            value={v4Mode}
            onChange={(e) => setV4Mode(e.target.value as V4Mode)}
            options={[
              { value: 'off', label: t('iface.modeOff') },
              { value: 'dhcp', label: 'DHCP' },
              { value: 'static', label: t('iface.modeStatic') },
            ]}
          />
        </FormGroup>
        {v4Mode === 'dhcp' && (
          <FormGrid>
            <FormGroup label={t('iface.hostname')} error={errors.hostname}>
              <Input value={hostname} onChange={(e) => setHostname(e.target.value)} placeholder={t('iface.optional')} />
            </FormGroup>
            <FormGroup label={t('iface.clientId')} error={errors.clientId}>
              <Input value={clientId} onChange={(e) => setClientId(e.target.value)} placeholder={t('iface.optional')} />
            </FormGroup>
          </FormGrid>
        )}
        {v4Mode === 'static' && (
          <>
            <FormGroup label={t('iface.addresses')} required error={errors.v4Addrs}>
              <OrderedList items={v4Addrs} onChange={setV4Addrs} options={[]} placeholder="192.168.1.20/24"
                addLabel={t('iface.addAddress')} deleteTitle={t('app.delete')} testId="iface-v4-addrs" />
            </FormGroup>
            <FormGroup label={t('iface.gateway')} error={errors.v4Gw}>
              <Input value={v4Gw} onChange={(e) => setV4Gw(e.target.value)} placeholder="192.168.1.1" data-testid="iface-v4-gw" />
            </FormGroup>
            <FormGroup label={t('iface.dns')} error={errors.v4Dns}>
              <OrderedList items={v4Dns} onChange={setV4Dns} options={[]} placeholder="1.1.1.1"
                addLabel={t('iface.addDns')} deleteTitle={t('app.delete')} testId="iface-v4-dns" />
            </FormGroup>
          </>
        )}

        {/* IPv6 */}
        <div className="iface-form-section">IPv6</div>
        <FormGroup label={t('iface.addrMode')}>
          <Select
            value={v6Mode}
            onChange={(e) => setV6Mode(e.target.value as V6Mode)}
            options={[
              { value: 'off', label: t('iface.modeOff') },
              { value: 'slaac', label: t('iface.modeAuto') },
              { value: 'static', label: t('iface.modeStatic') },
            ]}
          />
        </FormGroup>
        {v6Mode === 'static' && (
          <>
            <FormGroup label={t('iface.addresses')} required error={errors.v6Addrs}>
              <OrderedList items={v6Addrs} onChange={setV6Addrs} options={[]} placeholder="2001:db8::20/64"
                addLabel={t('iface.addAddress')} deleteTitle={t('app.delete')} testId="iface-v6-addrs" />
            </FormGroup>
            <FormGroup label={t('iface.gateway')} error={errors.v6Gw}>
              <Input value={v6Gw} onChange={(e) => setV6Gw(e.target.value)} placeholder="fe80::1" data-testid="iface-v6-gw" />
            </FormGroup>
            <FormGroup label={t('iface.dns')} error={errors.v6Dns}>
              <OrderedList items={v6Dns} onChange={setV6Dns} options={[]} placeholder="2606:4700:4700::1111"
                addLabel={t('iface.addDns')} deleteTitle={t('app.delete')} testId="iface-v6-dns" />
            </FormGroup>
          </>
        )}
      </div>
    </Modal>
  );
}
