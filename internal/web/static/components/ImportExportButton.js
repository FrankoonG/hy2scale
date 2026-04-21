import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useRef, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { Button, Modal, Toggle, useToast } from '@hy2scale/ui';
import * as XLSX from 'xlsx';
import * as api from '@/api';
export default function ImportExportButton({ target }) {
    const { t } = useTranslation();
    const toast = useToast();
    const queryClient = useQueryClient();
    const fileRef = useRef(null);
    const [importOpen, setImportOpen] = useState(false);
    const [overwrite, setOverwrite] = useState(false);
    const [processing, setProcessing] = useState(false);
    const [result, setResult] = useState('');
    const handleExport = useCallback(async () => {
        try {
            let data;
            let headers;
            let filename;
            switch (target) {
                case 'nodes': {
                    const clients = await api.getClients();
                    headers = ['name', 'addrs', 'password', 'sni', 'insecure', 'conn_mode', 'max_tx', 'max_rx'];
                    data = clients.map((c) => ({
                        name: c.name, addrs: (c.addrs || [c.addr]).join(','),
                        password: c.password, sni: c.sni || '', insecure: c.insecure ? 'yes' : 'no',
                        conn_mode: c.conn_mode || '', max_tx: c.max_tx || 0, max_rx: c.max_rx || 0,
                    }));
                    filename = 'nodes.xlsx';
                    break;
                }
                case 'users': {
                    const users = await api.getUsers();
                    headers = ['username', 'password', 'exit_via', 'exit_mode', 'traffic_limit_gb', 'expiry_date', 'enabled'];
                    data = users.map((u) => ({
                        username: u.username, password: u.password,
                        exit_via: u.exit_paths?.join(',') || u.exit_via || '',
                        exit_mode: u.exit_mode || '', traffic_limit_gb: u.traffic_limit ? u.traffic_limit / 1073741824 : 0,
                        expiry_date: u.expiry_date || '', enabled: u.enabled ? 'yes' : 'no',
                    }));
                    filename = 'users.xlsx';
                    break;
                }
                case 'rules': {
                    const { rules } = await api.getRules();
                    headers = ['id', 'name', 'type', 'targets', 'exit_via', 'exit_mode', 'enabled'];
                    data = rules.map((r) => ({
                        id: r.id, name: r.name, type: r.type,
                        targets: r.targets.join('\n'),
                        exit_via: r.exit_paths?.join(',') || r.exit_via || '',
                        exit_mode: r.exit_mode || '', enabled: r.enabled ? 'yes' : 'no',
                    }));
                    filename = 'rules.xlsx';
                    break;
                }
            }
            const ws = XLSX.utils.json_to_sheet(data, { header: headers });
            const wb = XLSX.utils.book_new();
            XLSX.utils.book_append_sheet(wb, ws, target);
            XLSX.writeFile(wb, filename);
            toast.success(t('import.exported'));
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [target, toast, t]);
    const handleImport = useCallback(async (file) => {
        setProcessing(true);
        setResult('');
        try {
            const buf = await file.arrayBuffer();
            const wb = XLSX.read(buf);
            const ws = wb.Sheets[wb.SheetNames[0]];
            const rows = XLSX.utils.sheet_to_json(ws);
            if (rows.length === 0) {
                toast.error(t('import.empty'));
                setProcessing(false);
                return;
            }
            let added = 0, skipped = 0, errors = 0;
            for (const row of rows) {
                try {
                    switch (target) {
                        case 'nodes': {
                            const addrs = String(row.addrs || row.addr || '').split(',').filter(Boolean);
                            const data = {
                                name: row.name, addr: addrs[0], addrs: addrs.length > 1 ? addrs : undefined,
                                password: row.password, sni: row.sni || undefined,
                                insecure: row.insecure === 'yes', conn_mode: row.conn_mode || undefined,
                                max_tx: Number(row.max_tx) || undefined, max_rx: Number(row.max_rx) || undefined,
                            };
                            if (overwrite) {
                                try {
                                    await api.updateClient(data.name, data);
                                    added++;
                                }
                                catch {
                                    await api.createClient(data);
                                    added++;
                                }
                            }
                            else {
                                try {
                                    await api.createClient(data);
                                    added++;
                                }
                                catch {
                                    skipped++;
                                }
                            }
                            break;
                        }
                        case 'users': {
                            const paths = String(row.exit_via || '').split(',').filter(Boolean);
                            const data = {
                                username: row.username, password: row.password,
                                exit_via: paths[0] || '', exit_paths: paths.length > 1 ? paths : undefined,
                                exit_mode: row.exit_mode || undefined,
                                traffic_limit: (Number(row.traffic_limit_gb) || 0) * 1073741824,
                                expiry_date: row.expiry_date || undefined,
                                enabled: row.enabled !== 'no',
                            };
                            if (overwrite) {
                                try {
                                    await api.createUser(data);
                                    added++;
                                }
                                catch {
                                    skipped++;
                                }
                            }
                            else {
                                try {
                                    await api.createUser(data);
                                    added++;
                                }
                                catch {
                                    skipped++;
                                }
                            }
                            break;
                        }
                        case 'rules': {
                            const paths = String(row.exit_via || '').split(',').filter(Boolean);
                            const data = {
                                id: row.id || undefined, name: row.name, type: row.type || 'ip',
                                targets: String(row.targets || '').split('\n').filter(Boolean),
                                exit_via: paths[0] || '', exit_paths: paths.length > 1 ? paths : undefined,
                                exit_mode: row.exit_mode || undefined,
                                enabled: row.enabled !== 'no',
                            };
                            if (overwrite) {
                                try {
                                    await api.updateRule(data.id, data);
                                    added++;
                                }
                                catch {
                                    await api.createRule(data);
                                    added++;
                                }
                            }
                            else {
                                try {
                                    await api.createRule(data);
                                    added++;
                                }
                                catch {
                                    skipped++;
                                }
                            }
                            break;
                        }
                    }
                }
                catch {
                    errors++;
                }
            }
            setResult(t('import.result', { added, skipped, errors }));
            queryClient.invalidateQueries({ queryKey: [target === 'nodes' ? 'topology' : target] });
        }
        catch (e) {
            toast.error(t('import.failed') + ': ' + String(e.message || e));
        }
        finally {
            setProcessing(false);
        }
    }, [target, overwrite, queryClient, toast, t]);
    return (_jsxs(_Fragment, { children: [_jsx(Button, { size: "sm", variant: "ghost", onClick: handleExport, children: t('import.export') }), _jsx(Button, { size: "sm", variant: "ghost", onClick: () => setImportOpen(true), children: t('import.import') }), _jsx(Modal, { open: importOpen, onClose: () => { setImportOpen(false); setResult(''); }, title: t(`import.import${target.charAt(0).toUpperCase() + target.slice(1)}`), footer: _jsx(Button, { onClick: () => { setImportOpen(false); setResult(''); }, children: t('app.close') }), children: _jsxs("div", { style: { display: 'flex', flexDirection: 'column', gap: 14 }, children: [_jsx("div", { style: {
                                border: '2px dashed var(--border)', borderRadius: 'var(--radius)',
                                padding: 30, textAlign: 'center', cursor: 'pointer', color: 'var(--text-muted)',
                            }, onClick: () => fileRef.current?.click(), onDrop: (e) => {
                                e.preventDefault();
                                const file = e.dataTransfer.files[0];
                                if (file)
                                    handleImport(file);
                            }, onDragOver: (e) => e.preventDefault(), children: processing ? t('import.processing') : t('import.dropHint') }), _jsx("input", { ref: fileRef, type: "file", accept: ".xlsx", style: { display: 'none' }, onChange: (e) => {
                                const file = e.target.files?.[0];
                                if (file)
                                    handleImport(file);
                                e.target.value = '';
                            } }), _jsxs("label", { style: { display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }, children: [_jsx(Toggle, { checked: overwrite, onChange: (e) => setOverwrite(e.target.checked), size: "sm" }), t('import.overwrite')] }), _jsx("div", { style: { fontSize: 11, color: 'var(--text-muted)' }, children: t('import.overwriteHint') }), result && _jsx("div", { style: { fontSize: 13, color: 'var(--green)', fontWeight: 500 }, children: result })] }) })] }));
}
