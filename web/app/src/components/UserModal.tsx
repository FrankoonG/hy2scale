import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import {
  Modal, Button, Input, PasswordInput, Toggle, Select, SegmentedControl, CheckSelect, FormGroup, FormGrid, FormStack, Hint, useToast,
} from '@hy2scale/ui';
import { ExitPathList, exitPathToApi, apiToExitPath, type ExitPathValue } from './ExitPathList';
import { PasswordRows, newRowId, type PasswordRow } from './users/PasswordRows';
import { userKind, usePolicySets, KIND_LABEL } from './users/userKind';
import { SubscriptionFields, type SubForm } from './users/SubscriptionFields';
import { resetUnit, todayISO as today } from './users/trafficReset';
import * as api from '@/api';
import type { UserConfig, TrafficReset } from '@/api';
import type { UserKind } from '@/api/types';
import { PROXY_REGISTRY } from '@/config/proxyRegistry';
import { useNodeStore } from '@/store/node';
import { useDialogInit } from '@/hooks/useFormInit';
import { useShareHost } from '@/hooks/useShareHost';
import { RateInput, rateToForm, formToRate, type RateValue, SizeInput, sizeToForm, formToSize, type SizeValue } from './RateInput';

const RESET_UNITS: TrafficReset[] = ['', 'daily', 'weekly', 'monthly'];
const RESET_EVERY_MAX = 1000;
const KINDS: UserKind[] = ['group', 'policy', 'access'];
const PROTOCOL_OPTIONS = PROXY_REGISTRY.map((p) => ({ value: p.key, label: p.label }));
/** The protocols a subscription lists, in its order (the name preview's {proto}). */
const SUB_PROTOCOLS = ['hy2', 'ss', 'trojan', 'socks5', 'http'];
const SUB_OFF: SubForm = { enabled: false, name: '', lan: true };

interface Props {
  open: boolean;
  onClose: () => void;
  // The user being edited, or null when creating a new one. Pass the
  // object directly rather than an id — the previous id-based shape
  // re-fetched the user list inside the modal effect, which raced
  // when the operator opened a modal while a previous fetch was
  // still in flight (state would briefly show the previous user's
  // exit_via, then snap to the right value once the fetch landed).
  // The page already has the list cached + refetched every 5 s, so
  // the object it holds is fresh enough.
  editing: UserConfig | null;
  animateFrom?: { x: number; y: number };
}

export default function UserModal({ open, onClose, editing, animateFrom }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();
  const sets = usePolicySets();
  const nodeName = useNodeStore((s) => s.node?.name) || '';
  const shareHost = useShareHost();

  const [loading, setLoading] = useState(false);
  const [username, setUsername] = useState('');
  // group / access kinds; kept (and sent) in the policy kind
  const [password, setPassword] = useState('');
  const [kind, setKind] = useState<UserKind>('group');
  const [rows, setRows] = useState<PasswordRow[]>([]);
  // proxyDisabled tracked as a Set for O(1) membership tests; the backend
  // stores it as a string[]. Empty set → user authenticates on every proxy.
  const [proxyDisabled, setProxyDisabled] = useState<Set<string>>(new Set());
  const [exitPath, setExitPath] = useState<ExitPathValue>({ paths: [''], mode: '' });
  const [trafficLimit, setTrafficLimit] = useState<SizeValue>(sizeToForm(0));
  const [rateUp, setRateUp] = useState<RateValue>(rateToForm(0));
  const [rateDown, setRateDown] = useState<RateValue>(rateToForm(0));
  const [expiryDate, setExpiryDate] = useState('');
  // traffic reset: every N units from the start date
  const [resetUnitV, setResetUnit] = useState<TrafficReset>('');
  const [resetEvery, setResetEvery] = useState('1');
  const [resetAnchor, setResetAnchor] = useState(today);
  const [sub, setSub] = useState<SubForm>(SUB_OFF);
  // the access kind's level; internal destinations (new users: denied)
  const [accessMode, setAccessMode] = useState<'restricted' | 'full'>('restricted');
  const [allowInternal, setAllowInternal] = useState(false);

  // Seed on open / on switching to another user only — the users list
  // refetches every 5 s and a user with traffic is a new object each flush.
  useDialogInit(open, editing?.id ?? null, () => {
    if (!editing) {
      setUsername(''); setPassword(''); setKind('group'); setRows([]);
      setProxyDisabled(new Set());
      setExitPath({ paths: [''], mode: '' });
      setTrafficLimit(sizeToForm(0)); setExpiryDate('');
      setRateUp(rateToForm(0)); setRateDown(rateToForm(0));
      setResetUnit(''); setResetEvery('1'); setResetAnchor(today());
      setSub(SUB_OFF);
      setAccessMode('restricted'); setAllowInternal(false);
      return;
    }
    setUsername(editing.username);
    setPassword(editing.password);
    setKind(userKind(editing));
    setRows((editing.rows || []).map((r) => ({ id: r.id, password: r.password, policy: editing.row_policy?.[r.id] || '' })));
    setProxyDisabled(new Set(editing.proxy_disabled || []));
    setExitPath(apiToExitPath(editing.exit_via, editing.exit_paths, editing.exit_mode));
    setTrafficLimit(sizeToForm(editing.traffic_limit));
    setRateUp(rateToForm(editing.rate_up)); setRateDown(rateToForm(editing.rate_down));
    setExpiryDate(editing.expiry_date || '');
    setResetUnit(resetUnit(editing.traffic_reset));
    setResetEvery(String(editing.traffic_reset_every || 1));
    setResetAnchor(editing.traffic_reset_anchor || today());
    setSub({ enabled: !!editing.sub?.token, name: editing.sub?.name || '', lan: !editing.sub?.no_lan });
    setAccessMode(editing.node_access_mode === 'full' ? 'full' : 'restricted');
    setAllowInternal(!editing.deny_internal);
  });

  // the policy kind starts with one row: the current password
  const pickKind = (k: UserKind) => {
    setKind(k);
    if (k === 'policy' && rows.length === 0) setRows([{ id: newRowId([]), password, policy: sets[0]?.id ?? '' }]);
  };

  const handleSubmit = async () => {
    const policyKind = kind === 'policy';
    if (!username || (!policyKind && !password)) {
      toast.error(t('users.usernamePassRequired'));
      return;
    }
    if (policyKind) {
      if (rows.length === 0 || rows.some((r) => !r.password)) {
        toast.error(t('users.rowPasswordRequired'));
        return;
      }
      if (new Set(rows.map((r) => r.password)).size !== rows.length) {
        toast.error(t('users.rowPasswordDup'));
        return;
      }
    }
    // Block re-using the hy2 server password as a password the user signs
    // in with here — peers see the server password in plain text via the
    // clients[] block, so this would silently leak the user's credentials
    // to every other peer. Backend enforces the same rule, this is the UI hint.
    const serverPw = useNodeStore.getState().node?.server?.password;
    const signIn = policyKind ? rows.filter((r) => r.policy).map((r) => r.password) : [password];
    if (serverPw && signIn.includes(serverPw)) {
      toast.error(t('users.passwordCollidesServer'));
      return;
    }
    const every = Number(resetEvery);
    if (resetUnitV && !(Number.isInteger(every) && every >= 1 && every <= RESET_EVERY_MAX)) {
      toast.error(t('users.resetEveryInvalid', { max: RESET_EVERY_MAX }));
      return;
    }
    setLoading(true);
    const proxyDisabledArr = Array.from(proxyDisabled);
    const data: Partial<UserConfig> = {
      username,
      password,
      exit_kind: kind,
      rows: policyKind ? rows.map((r) => ({ id: r.id, password: r.password })) : undefined,
      row_policy: policyKind ? Object.fromEntries(rows.filter((r) => r.policy).map((r) => [r.id, r.policy])) : undefined,
      proxy_disabled: proxyDisabledArr,
      ...exitPathToApi(exitPath),
      traffic_limit: formToSize(trafficLimit),
      rate_up: formToRate(rateUp),
      rate_down: formToRate(rateDown),
      expiry_date: expiryDate || undefined,
      traffic_reset: resetUnitV,
      traffic_reset_every: resetUnitV ? every : undefined,
      traffic_reset_anchor: resetUnitV ? resetAnchor || today() : undefined,
      // enabled is set from the users list; an edit keeps it
      enabled: editing ? undefined : true,
      node_access_mode: kind === 'access' ? accessMode : undefined,
      deny_internal: !allowInternal,
      // node access users have no subscription
      sub: kind === 'access' ? undefined : { enabled: sub.enabled, name: sub.name.trim(), no_lan: !sub.lan },
    };

    try {
      if (editing) {
        await api.updateUser(editing.id, data);
        toast.success(t('users.updated', { name: username }));
      } else {
        await api.createUser(data);
        toast.success(t('users.added', { name: username }));
      }
      queryClient.invalidateQueries({ queryKey: ['users'] });
      queryClient.invalidateQueries({ queryKey: ['userConflicts'] });
      queryClient.invalidateQueries({ queryKey: ['userLinks'] });
      queryClient.invalidateQueries({ queryKey: ['userSub'] });
      onClose();
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  const title = editing ? t('users.editPrefix', { name: username }) : t('users.addTitle');

  const protocolsOn = PROXY_REGISTRY.filter((p) => !proxyDisabled.has(p.key)).map((p) => p.key);
  // at least one protocol stays on
  const setProtocols = (on: string[]) => {
    if (on.length) setProxyDisabled(new Set(PROXY_REGISTRY.map((p) => p.key).filter((k) => !on.includes(k))));
  };
  // the subscription name preview's sample entry
  const firstPolicy = kind === 'policy' && rows[0]?.policy ? sets.find((x) => x.id === rows[0].policy)?.name ?? rows[0].policy : '';
  const subSample = {
    user: username, node: nodeName, policy: firstPolicy,
    proto: SUB_PROTOCOLS.find((p) => !proxyDisabled.has(p)) || '', lan: '', host: shareHost,
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          <Button variant="primary" onClick={handleSubmit} loading={loading}>{t('app.save')}</Button>
        </>
      }
    >
      <FormStack gap={14}>
        <FormGrid>
          <FormGroup label={t('users.username')} required>
            <Input value={username} onChange={(e) => setUsername(e.target.value)} />
          </FormGroup>
          {kind !== 'policy' && (
            <FormGroup label={t('users.password')} required>
              <PasswordInput value={password} onChange={(e) => setPassword(e.target.value)} onGenerate={setPassword} />
            </FormGroup>
          )}
        </FormGrid>

        <FormGroup label={t('users.exitKind')}>
          <div className="exit-choice" data-testid="user-exit-kind">
            <SegmentedControl<UserKind>
              items={KINDS.map((k) => ({
                key: k, label: t(KIND_LABEL[k]), testId: `user-kind-${k}`,
                // no policy set to choose on this node
                disabled: k === 'policy' && sets.length === 0 && kind !== 'policy',
              }))}
              value={kind}
              onChange={pickKind}
            />
            {kind === 'group' && <ExitPathList label="" value={exitPath} onChange={setExitPath} />}
            {kind === 'policy' && <PasswordRows rows={rows} onChange={setRows} sets={sets} />}
            {kind === 'access' && (
              <>
                <div className="user-access-mode" data-testid="user-access-mode">
                  <Select
                    value={accessMode}
                    onChange={(e) => setAccessMode(e.target.value === 'full' ? 'full' : 'restricted')}
                    options={[
                      { value: 'restricted', label: t('users.accessRestricted') },
                      { value: 'full', label: t('users.accessFull') },
                    ]}
                  />
                </div>
                <Hint>{t('users.nodeAccessHint')}</Hint>
              </>
            )}
          </div>
        </FormGroup>

        <FormGrid>
          <FormGroup label={t('users.rateUp')}>
            <RateInput value={rateUp} onChange={setRateUp} testId="user-rate-up" />
          </FormGroup>
          <FormGroup label={t('users.rateDown')}>
            <RateInput value={rateDown} onChange={setRateDown} testId="user-rate-down" />
          </FormGroup>
        </FormGrid>

        <FormGrid>
          <FormGroup label={t('users.trafficReset')}>
            <div className="hy-rate-input user-reset">
              {resetUnitV && (
                <Input type="number" min={1} max={RESET_EVERY_MAX} step={1} value={resetEvery} placeholder="1"
                  onChange={(e) => setResetEvery(e.target.value)} data-testid="user-reset-every" />
              )}
              <div className="user-reset-unit" data-testid="user-reset-unit">
                <Select
                  value={resetUnitV}
                  onChange={(e) => setResetUnit(e.target.value as TrafficReset)}
                  options={RESET_UNITS.map((u) => ({ value: u, label: u ? t(`users.resetUnit.${u}`) : t('users.reset.never') }))}
                />
              </div>
            </div>
          </FormGroup>
          {resetUnitV && (
            <FormGroup label={t('users.resetAnchor')}>
              <Input type="date" value={resetAnchor} onChange={(e) => setResetAnchor(e.target.value)} data-testid="user-reset-anchor" />
            </FormGroup>
          )}
        </FormGrid>

        <FormGrid>
          <FormGroup label={t('users.trafficLimit')}>
            <SizeInput value={trafficLimit} onChange={setTrafficLimit} testId="user-traffic-limit" />
          </FormGroup>
          <FormGroup label={t('users.expiryDate')}>
            <Input type="date" value={expiryDate} onChange={(e) => setExpiryDate(e.target.value)} />
          </FormGroup>
        </FormGrid>

        <FormGrid>
          <FormGroup label={t('users.allowInternal')} hint={t('users.allowInternalHint')}>
            <Toggle checked={allowInternal} onChange={(e) => setAllowInternal(e.target.checked)} data-testid="user-allow-internal" />
          </FormGroup>
        </FormGrid>

        {kind !== 'access' && (
          <FormGroup label={t('sub.protocols')}>
            <CheckSelect testId="user-protocols" label={t('sub.protocolsAll')} maxNames={3}
              options={PROTOCOL_OPTIONS} value={protocolsOn} onChange={setProtocols} />
          </FormGroup>
        )}
        {kind !== 'access' && <SubscriptionFields value={sub} onChange={setSub} sample={subSample} />}
      </FormStack>
    </Modal>
  );
}
