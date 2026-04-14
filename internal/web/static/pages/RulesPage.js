import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Table, Toggle, Badge, Modal, Input, Select, Textarea, FormGroup, useToast, useConfirm, useSelection, } from '@hy2scale/ui';
import { ExitPathList, exitPathToApi, apiToExitPath } from '@/components/ExitPathList';
import { ExitViaCell } from '@/components/ExitViaCell';
import ImportExportButton from '@/components/ImportExportButton';
import BulkActionBar from '@/components/BulkActionBar';
import * as api from '@/api';
export default function RulesPage() {
    const { t } = useTranslation();
    const toast = useToast();
    const confirmDlg = useConfirm();
    const queryClient = useQueryClient();
    const { data } = useQuery({ queryKey: ['rules'], queryFn: api.getRules });
    const { data: tunMode } = useQuery({ queryKey: ['tunMode'], queryFn: api.getTunMode });
    const rules = data?.rules || [];
    const available = data?.available !== false;
    const selection = useSelection(rules.map((r) => r.id));
    const [modalOpen, setModalOpen] = useState(false);
    const [editId, setEditId] = useState(null);
    const [clickPos, setClickPos] = useState();
    // Rule form
    const [name, setName] = useState('');
    const [ruleType, setRuleType] = useState('ip');
    const [targets, setTargets] = useState('');
    const [exitPath, setExitPath] = useState({ paths: [''], mode: '' });
    const [ruleEnabled, setRuleEnabled] = useState(true);
    const [saving, setSaving] = useState(false);
    const openAdd = (e) => {
        setClickPos({ x: e.clientX, y: e.clientY });
        setEditId(null);
        setName('');
        setRuleType('ip');
        setTargets('');
        setExitPath({ paths: [''], mode: '' });
        setRuleEnabled(true);
        setModalOpen(true);
    };
    const openEdit = (r, e) => {
        setClickPos({ x: e.clientX, y: e.clientY });
        setEditId(r.id);
        setName(r.name);
        setRuleType(r.type);
        setTargets(r.targets.join('\n'));
        setExitPath(apiToExitPath(r.exit_via, r.exit_paths, r.exit_mode));
        setRuleEnabled(r.enabled);
        setModalOpen(true);
    };
    const handleSave = async () => {
        const targetList = targets.split('\n').map((l) => l.trim()).filter(Boolean);
        if (targetList.length === 0) {
            toast.error(t('rules.targetsRequired'));
            return;
        }
        const exitData = exitPathToApi(exitPath);
        if (!exitData.exit_via) {
            toast.error(t('rules.exitRequired'));
            return;
        }
        setSaving(true);
        try {
            const ruleData = { name, type: ruleType, targets: targetList, ...exitData, enabled: ruleEnabled };
            if (editId) {
                await api.updateRule(editId, ruleData);
            }
            else {
                await api.createRule(ruleData);
            }
            toast.success(t('rules.saved'));
            queryClient.invalidateQueries({ queryKey: ['rules'] });
            setModalOpen(false);
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
        finally {
            setSaving(false);
        }
    };
    // Bulk actions
    const bulkToggle = useCallback(async (enabled) => {
        try {
            await Promise.all([...selection.selected].map((id) => api.toggleRule(id, enabled)));
            toast.success(`${enabled ? t('app.bulkEnable') : t('app.bulkDisable')}: ${selection.count}`);
            queryClient.invalidateQueries({ queryKey: ['rules'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [selection, queryClient, toast, t]);
    const bulkDelete = useCallback(async () => {
        const ok = await confirmDlg({
            title: t('app.bulkDelete'), message: t('rules.deleteConfirm'),
            danger: true, confirmText: t('app.delete'), cancelText: t('app.cancel'),
        });
        if (!ok)
            return;
        try {
            await Promise.all([...selection.selected].map((id) => api.deleteRule(id)));
            toast.success(`${t('app.bulkDelete')}: ${selection.count}`);
            queryClient.invalidateQueries({ queryKey: ['rules'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [selection, confirmDlg, queryClient, toast, t]);
    // TUN mode handlers
    const handleTunToggle = async () => {
        try {
            await api.updateTunMode({ enabled: !tunMode?.enabled });
            toast.success(tunMode?.enabled ? t('rules.tunDisabled') : t('rules.tunEnabled'));
            queryClient.invalidateQueries({ queryKey: ['tunMode'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    };
    const handleTunMode = async (mode) => {
        try {
            await api.updateTunMode({ mode });
            toast.success(t('rules.tunModeChanged'));
            queryClient.invalidateQueries({ queryKey: ['tunMode'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    };
    if (!available) {
        return (_jsx(Card, { title: t('rules.title'), children: _jsx("div", { style: { color: 'var(--text-muted)', fontSize: 13 }, children: t('rules.unavailable') }) }));
    }
    const columns = [
        { key: 'name', title: t('rules.name'), render: (r) => _jsx("strong", { children: r.name }) },
        { key: 'type', title: '', width: '70px', render: (r) => _jsx(Badge, { variant: r.type === 'ip' ? 'blue' : 'orange', children: r.type === 'ip' ? 'IP' : 'Domain' }) },
        { key: 'targets', title: t('rules.targets'), render: (r) => _jsxs("span", { className: "mono", style: { fontSize: 11 }, children: [r.targets.slice(0, 3).join(', '), r.targets.length > 3 ? ` +${r.targets.length - 3}` : ''] }) },
        { key: 'exit', title: t('rules.exitVia'), render: (r) => _jsx(ExitViaCell, { exitVia: r.exit_via, exitPaths: r.exit_paths, exitMode: r.exit_mode }) },
        {
            key: 'actions', title: '', width: '40px', render: (r) => (_jsx("button", { className: "hy-row-edit", onClick: (e) => openEdit(r, e), title: t('app.edit'), children: _jsxs("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: "2", children: [_jsx("path", { d: "M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7" }), _jsx("path", { d: "M18.5 2.5a2.12 2.12 0 013 3L12 15l-4 1 1-4 9.5-9.5z" })] }) })),
        },
    ];
    return (_jsxs("div", { style: { display: 'flex', flexDirection: 'column', gap: 20 }, children: [_jsx(Card, { title: t('rules.tunMode'), children: _jsxs("div", { style: { display: 'flex', flexDirection: 'column', gap: 12 }, children: [_jsx("div", { style: { fontSize: 12, color: 'var(--text-muted)' }, children: t('rules.tunDesc') }), _jsxs("div", { style: { display: 'flex', alignItems: 'center', gap: 12 }, children: [_jsx(Toggle, { checked: tunMode?.enabled || false, onChange: handleTunToggle }), _jsx(Badge, { variant: tunMode?.enabled ? (tunMode.status === 'active' ? 'green' : 'orange') : 'muted', children: tunMode?.enabled ? (tunMode.status === 'active' ? t('rules.tunActive') : t('rules.tunStarting')) : t('rules.tunInactive') })] }), tunMode?.enabled && (_jsxs("div", { style: { display: 'flex', gap: 12, fontSize: 12 }, children: [_jsxs("label", { style: { display: 'flex', alignItems: 'center', gap: 4, cursor: 'pointer' }, children: [_jsx("input", { type: "radio", checked: tunMode.mode === 'mixed', onChange: () => handleTunMode('mixed') }), t('rules.tunModeMixed')] }), _jsxs("label", { style: { display: 'flex', alignItems: 'center', gap: 4, cursor: 'pointer' }, children: [_jsx("input", { type: "radio", checked: tunMode.mode === 'full', onChange: () => handleTunMode('full') }), t('rules.tunModeFull')] })] }))] }) }), _jsx(Card, { title: t('rules.title'), count: rules.length, actions: _jsxs("div", { style: { display: 'flex', gap: 8, alignItems: 'center' }, children: [_jsx(BulkActionBar, { count: selection.count, onClear: selection.clear, children: (() => {
                                const sel = [...selection.selected];
                                const items = sel.map((id) => rules.find((r) => r.id === id)).filter(Boolean);
                                const hasDisabled = items.some((r) => !r.enabled);
                                const hasEnabled = items.some((r) => r.enabled);
                                return _jsxs(_Fragment, { children: [hasDisabled && _jsx(Button, { size: "sm", onClick: () => bulkToggle(true), children: t('app.bulkEnable') }), hasEnabled && _jsx(Button, { size: "sm", onClick: () => bulkToggle(false), children: t('app.bulkDisable') }), _jsx(Button, { size: "sm", variant: "danger", onClick: bulkDelete, children: t('app.bulkDelete') })] });
                            })() }), _jsx(ImportExportButton, { target: "rules" }), _jsx(Button, { size: "sm", variant: "primary", onClick: openAdd, children: t('rules.newRule') })] }), noPadding: true, children: _jsx(Table, { columns: columns, data: rules, rowKey: (r) => r.id, rowClassName: (r) => !r.enabled ? 'disabled-row' : undefined, emptyText: t('rules.noRules'), selection: selection }) }), _jsx(Modal, { open: modalOpen, onClose: () => setModalOpen(false), title: editId ? t('app.edit') : t('rules.newRule'), animateFrom: clickPos, footer: _jsxs(_Fragment, { children: [_jsx(Button, { onClick: () => setModalOpen(false), children: t('app.cancel') }), _jsx(Button, { variant: "primary", onClick: handleSave, loading: saving, children: t('app.save') })] }), children: _jsxs("div", { style: { display: 'flex', flexDirection: 'column', gap: 14 }, children: [_jsx(FormGroup, { label: t('rules.name'), children: _jsx(Input, { value: name, onChange: (e) => setName(e.target.value) }) }), _jsx(FormGroup, { label: "Type", children: _jsx(Select, { value: ruleType, onChange: (e) => setRuleType(e.target.value), options: [
                                    { value: 'ip', label: t('rules.ipRules') },
                                    { value: 'domain', label: t('rules.domainRules') },
                                ] }) }), _jsx(FormGroup, { label: ruleType === 'ip' ? t('rules.ipTargets') : t('rules.domainTargets'), required: true, children: _jsx(Textarea, { value: targets, onChange: (e) => setTargets(e.target.value), rows: 5, monospace: true, placeholder: ruleType === 'ip' ? '1.2.3.0/24\n5.6.7.8' : 'example.com\n*.test.com' }) }), _jsx(ExitPathList, { value: exitPath, onChange: setExitPath, label: t('rules.exitVia') }), _jsx(FormGroup, { label: t('app.enabled'), children: _jsx(Toggle, { checked: ruleEnabled, onChange: (e) => setRuleEnabled(e.target.checked) }) })] }) })] }));
}
