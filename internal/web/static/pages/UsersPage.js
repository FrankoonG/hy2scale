import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, useToast, useConfirm, useSelection } from '@hy2scale/ui';
import * as api from '@/api';
import { fmtBytes } from '@/hooks/useFormat';
import { ExitViaCell } from '@/components/ExitViaCell';
import UserModal from '@/components/UserModal';
import ImportExportButton from '@/components/ImportExportButton';
import BulkActionBar from '@/components/BulkActionBar';
export default function UsersPage() {
    const { t } = useTranslation();
    const toast = useToast();
    const confirm = useConfirm();
    const queryClient = useQueryClient();
    const [modalOpen, setModalOpen] = useState(false);
    const [editingId, setEditingId] = useState(null);
    const [clickPos, setClickPos] = useState();
    const { data: users = [] } = useQuery({ queryKey: ['users'], queryFn: api.getUsers, refetchInterval: 5000 });
    const { data: sessions = [] } = useQuery({ queryKey: ['sessions'], queryFn: api.getSessions, refetchInterval: 3000 });
    const openAdd = (e) => {
        setClickPos({ x: e.clientX, y: e.clientY });
        setEditingId(null);
        setModalOpen(true);
    };
    const openEdit = (id, e) => {
        setClickPos({ x: e.clientX, y: e.clientY });
        setEditingId(id);
        setModalOpen(true);
    };
    const handleToggle = useCallback(async (u) => {
        try {
            await api.toggleUser(u.id, !u.enabled);
            queryClient.invalidateQueries({ queryKey: ['users'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [queryClient, toast]);
    const handleReset = useCallback(async (u) => {
        const ok = await confirm({
            title: t('users.resetTitle'),
            message: t('users.resetConfirm'),
            confirmText: t('users.reset'),
            cancelText: t('app.cancel'),
        });
        if (!ok)
            return;
        try {
            await api.resetTraffic(u.id);
            toast.success(t('users.trafficReset'));
            queryClient.invalidateQueries({ queryKey: ['users'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [confirm, t, queryClient, toast]);
    const handleDelete = useCallback(async (u) => {
        const ok = await confirm({
            title: t('users.deleteTitle'),
            message: t('users.deleteConfirm', { name: u.username }),
            danger: true, confirmText: t('app.delete'), cancelText: t('app.cancel'),
        });
        if (!ok)
            return;
        try {
            await api.deleteUser(u.id);
            toast.success(t('users.deleted', { name: u.username }));
            queryClient.invalidateQueries({ queryKey: ['users'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [confirm, t, queryClient, toast]);
    const handleKick = useCallback(async (s) => {
        try {
            await api.kickSession(s.key);
            toast.success(t('devices.kicked'));
            queryClient.invalidateQueries({ queryKey: ['sessions'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [queryClient, toast, t]);
    const isExpired = (date) => {
        if (!date)
            return false;
        try {
            return new Date(date) < new Date();
        }
        catch {
            return false;
        }
    };
    const selection = useSelection(users.map((u) => u.id));
    const bulkToggle = useCallback(async (enabled) => {
        try {
            await Promise.all([...selection.selected].map((id) => api.toggleUser(id, enabled)));
            toast.success(`${enabled ? t('app.bulkEnable') : t('app.bulkDisable')}: ${selection.count}`);
            queryClient.invalidateQueries({ queryKey: ['users'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [selection, queryClient, toast, t]);
    const bulkReset = useCallback(async () => {
        const ok = await confirm({
            title: t('users.resetTitle'), message: t('users.resetConfirm'),
            confirmText: t('users.reset'), cancelText: t('app.cancel'),
        });
        if (!ok)
            return;
        try {
            await Promise.all([...selection.selected].map((id) => api.resetTraffic(id)));
            toast.success(`${t('users.bulkResetTraffic')}: ${selection.count}`);
            queryClient.invalidateQueries({ queryKey: ['users'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [selection, confirm, queryClient, toast, t]);
    const bulkDelete = useCallback(async () => {
        const ok = await confirm({
            title: t('app.bulkDelete'), message: t('users.deleteConfirm', { name: `${selection.count} users` }),
            danger: true, confirmText: t('app.delete'), cancelText: t('app.cancel'),
        });
        if (!ok)
            return;
        try {
            await Promise.all([...selection.selected].map((id) => api.deleteUser(id)));
            toast.success(`${t('app.bulkDelete')}: ${selection.count}`);
            queryClient.invalidateQueries({ queryKey: ['users'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [selection, confirm, queryClient, toast, t]);
    return (_jsxs("div", { style: { display: 'flex', flexDirection: 'column', gap: 20 }, children: [_jsx(Card, { title: t('users.title'), count: users.length, actions: _jsxs("div", { style: { display: 'flex', gap: 8, alignItems: 'center' }, children: [_jsx(BulkActionBar, { count: selection.count, onClear: selection.clear, children: (() => {
                                const sel = [...selection.selected];
                                const items = sel.map((id) => users.find((u) => u.id === id)).filter(Boolean);
                                const hasDisabled = items.some((u) => !u.enabled);
                                const hasEnabled = items.some((u) => u.enabled);
                                return _jsxs(_Fragment, { children: [hasDisabled && _jsx(Button, { size: "sm", onClick: () => bulkToggle(true), children: t('app.bulkEnable') }), hasEnabled && _jsx(Button, { size: "sm", onClick: () => bulkToggle(false), children: t('app.bulkDisable') }), _jsx(Button, { size: "sm", onClick: bulkReset, children: t('users.bulkResetTraffic') }), _jsx(Button, { size: "sm", variant: "danger", onClick: bulkDelete, children: t('app.bulkDelete') })] });
                            })() }), _jsx(ImportExportButton, { target: "users" }), _jsx(Button, { size: "sm", variant: "primary", onClick: openAdd, children: t('users.addUser') })] }), noPadding: true, children: users.length === 0 ? (_jsx("div", { className: "hy-empty", dangerouslySetInnerHTML: { __html: t('users.noUsers') } })) : (_jsx("div", { className: "hy-table-wrap", children: _jsxs("table", { className: "hy-table", children: [_jsx("thead", { children: _jsxs("tr", { children: [_jsx("th", { className: "col-check", children: _jsx("input", { type: "checkbox", checked: selection.isAllSelected, ref: (el) => { if (el)
                                                    el.indeterminate = selection.isSomeSelected; }, onChange: selection.toggleAll }) }), _jsx("th", { style: { width: 120 }, children: t('users.username') }), _jsx("th", { style: { minWidth: 180 }, children: t('users.exitVia') }), _jsx("th", { style: { width: 130, textAlign: 'right' }, children: t('users.traffic') }), _jsx("th", { style: { width: 90, textAlign: 'right' }, children: t('users.expiry') }), _jsx("th", { style: { width: 40 } })] }) }), _jsx("tbody", { children: users.map((u) => {
                                    const limitGB = u.traffic_limit > 0 ? (u.traffic_limit / 1073741824).toFixed(1) + ' GB' : '∞';
                                    const usedGB = (u.traffic_used / 1073741824).toFixed(2) + ' GB';
                                    const pct = u.traffic_limit > 0 ? Math.min(100, (u.traffic_used / u.traffic_limit * 100)) : 0;
                                    const expired = isExpired(u.expiry_date);
                                    const isSelected = selection.selected.has(u.id);
                                    return (_jsxs("tr", { className: `${!u.enabled ? 'disabled-row' : ''}${isSelected ? ' selected' : ''}`, children: [_jsx("td", { className: "col-check", children: _jsx("input", { type: "checkbox", checked: isSelected, onChange: () => selection.toggle(u.id) }) }), _jsx("td", { children: _jsx("b", { children: u.username }) }), _jsx("td", { children: _jsx(ExitViaCell, { exitVia: u.exit_via, exitPaths: u.exit_paths, exitMode: u.exit_mode }) }), _jsxs("td", { style: { textAlign: 'right' }, children: [_jsxs("span", { style: { fontSize: 12 }, children: [usedGB, " / ", limitGB] }), u.traffic_limit > 0 && (_jsx("div", { style: { background: 'var(--border-light)', height: 3, borderRadius: 2, marginTop: 3 }, children: _jsx("div", { style: { background: pct > 90 ? 'var(--red)' : 'var(--primary)', height: '100%', width: `${pct}%`, borderRadius: 2 } }) }))] }), _jsx("td", { style: { textAlign: 'right' }, children: _jsx("span", { style: { fontSize: 12, color: expired ? 'var(--red)' : undefined }, children: u.expiry_date || '—' }) }), _jsx("td", { children: _jsx("button", { className: "hy-row-edit", onClick: (e) => openEdit(u.id, e), title: t('app.edit'), children: _jsxs("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: "2", children: [_jsx("path", { d: "M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7" }), _jsx("path", { d: "M18.5 2.5a2.12 2.12 0 013 3L12 15l-4 1 1-4 9.5-9.5z" })] }) }) })] }, u.id));
                                }) })] }) })) }), _jsx(Card, { title: t('devices.title'), count: sessions.length, noPadding: true, children: sessions.length === 0 ? (_jsx("div", { className: "hy-empty", children: t('devices.noDevices') })) : (_jsx("div", { className: "hy-table-wrap", children: _jsxs("table", { className: "hy-table", children: [_jsx("thead", { children: _jsxs("tr", { children: [_jsx("th", { children: t('devices.user') }), _jsx("th", { children: t('devices.ip') }), _jsx("th", { children: t('devices.proxy') }), _jsx("th", { children: t('devices.conn') }), _jsx("th", { children: t('devices.traffic') }), _jsx("th", { children: t('devices.duration') }), _jsx("th", { style: { width: 60 } })] }) }), _jsx("tbody", { children: sessions.map((s) => (_jsxs("tr", { children: [_jsx("td", { children: _jsx("b", { children: s.user }) }), _jsx("td", { children: _jsx("span", { className: "mono", children: s.ip }) }), _jsx("td", { children: s.proxy }), _jsx("td", { children: s.conns }), _jsx("td", { children: _jsx("span", { className: "mono", children: fmtBytes(s.tx_bytes + s.rx_bytes) }) }), _jsx("td", { children: s.duration }), _jsx("td", { children: _jsx(Button, { size: "sm", variant: "danger", onClick: () => handleKick(s), children: t('devices.kick') }) })] }, s.key))) })] }) })) }), _jsx(UserModal, { open: modalOpen, onClose: () => setModalOpen(false), editingId: editingId, animateFrom: clickPos })] }));
}
