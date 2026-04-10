import { jsx as _jsx, Fragment as _Fragment, jsxs as _jsxs } from "react/jsx-runtime";
import { useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Badge, Tabs, Table, Modal, Input, Textarea, Select, FormGroup, FormGrid, TabPanel, useToast, useConfirm, useSelection, } from '@hy2scale/ui';
import BulkActionBar from '@/components/BulkActionBar';
import * as api from '@/api';
const PEM_EXTS = ['.pem', '.crt', '.cer', '.key', '.pub', '.txt'];
const MAX_FILE = 64 * 1024;
function readPemFile(file) {
    return new Promise((resolve, reject) => {
        if (file.size > MAX_FILE) {
            reject('File too large');
            return;
        }
        const ext = file.name.substring(file.name.lastIndexOf('.')).toLowerCase();
        if (!PEM_EXTS.includes(ext)) {
            reject('Unsupported file type');
            return;
        }
        const r = new FileReader();
        r.onload = () => resolve(r.result);
        r.onerror = () => reject('Read error');
        r.readAsText(file);
    });
}
export default function TLSPage() {
    const { t } = useTranslation();
    const toast = useToast();
    const confirm = useConfirm();
    const queryClient = useQueryClient();
    const { data: certs = [] } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts });
    const [modalOpen, setModalOpen] = useState(false);
    const [certTab, setCertTab] = useState('paste');
    const [clickPos, setClickPos] = useState();
    const [saving, setSaving] = useState(false);
    const [generating, setGenerating] = useState(false);
    // Common fields
    const [certId, setCertId] = useState('');
    const [certName, setCertName] = useState('');
    const [editMode, setEditMode] = useState(false);
    // CA signing
    const [caId, setCaId] = useState('');
    const [cn, setCn] = useState('');
    // Manual input (paste)
    const [certPem, setCertPem] = useState('');
    const [keyPem, setKeyPem] = useState('');
    // Drag-drop PEM file handler
    const makeDrop = useCallback((setter) => ({
        onDragOver: (e) => { e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; },
        onDrop: (e) => {
            e.preventDefault();
            const file = e.dataTransfer.files[0];
            if (file)
                readPemFile(file).then(setter).catch((err) => toast.error(String(err)));
        },
    }), [toast]);
    // File path
    const [certPath, setCertPath] = useState('');
    const [keyPath, setKeyPath] = useState('');
    const openNew = (e) => {
        setEditMode(false);
        setClickPos({ x: e.clientX, y: e.clientY });
        setCertId('');
        setCertName('');
        setCertPem('');
        setKeyPem('');
        setCertPath('');
        setKeyPath('');
        setCaId('');
        setCn('');
        setCertTab('paste');
        setModalOpen(true);
    };
    const handleEdit = async (cert, e) => {
        setClickPos({ x: e.clientX, y: e.clientY });
        setCertId(cert.id);
        setCertName(cert.name || '');
        setCertPem('');
        setKeyPem('');
        setCertPath('');
        setKeyPath('');
        setCaId('');
        setCn('');
        setCertTab('paste');
        setEditMode(true);
        try {
            const pem = await api.getCertPem(cert.id);
            if (pem.cert)
                setCertPem(pem.cert);
            if (pem.key)
                setKeyPem(pem.key);
        }
        catch { /* ignore */ }
        setModalOpen(true);
    };
    const handleGenerate = async () => {
        if (!certId) {
            toast.error(t('tls.fillIdFirst'));
            return;
        }
        setGenerating(true);
        try {
            if (caId) {
                await api.signCert({ ca_id: caId, id: certId, name: certName, cn: cn || certId, days: 7300 });
            }
            else {
                await api.generateCert({ id: certId, name: certName, domains: [certId], days: 3650 });
            }
            // Fetch generated PEM to fill textareas — don't refresh cert list yet
            const pem = await api.getCertPem(certId);
            setCertPem(pem.cert || '');
            setKeyPem(pem.key || '');
            setCertTab('paste');
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
        finally {
            setGenerating(false);
        }
    };
    const handleSave = async () => {
        if (!certId) {
            toast.error(t('tls.idRequired'));
            return;
        }
        setSaving(true);
        try {
            if (certTab === 'paste') {
                if (!certPem) {
                    toast.error(t('tls.certPemRequired'));
                    setSaving(false);
                    return;
                }
                await api.importCert({ id: certId, name: certName, cert: certPem, key: keyPem || undefined });
            }
            else {
                if (!certPath) {
                    toast.error(t('tls.certPathRequired'));
                    setSaving(false);
                    return;
                }
                await api.importCertPath({ id: certId, name: certName, cert_path: certPath, key_path: keyPath || undefined });
            }
            toast.success(t('tls.certSaved'));
            queryClient.invalidateQueries({ queryKey: ['certs'] });
            setModalOpen(false);
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
        finally {
            setSaving(false);
        }
    };
    const handleDelete = async (cert) => {
        const ok = await confirm({
            title: t('tls.deleteTitle'),
            message: t('tls.deleteConfirm', { id: cert.id }),
            danger: true, confirmText: t('app.delete'), cancelText: t('app.cancel'),
        });
        if (!ok)
            return;
        try {
            await api.deleteCert(cert.id);
            toast.success(t('tls.deleted', { id: cert.id }));
            queryClient.invalidateQueries({ queryKey: ['certs'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    };
    const isExpired = (date) => {
        try {
            return new Date(date) < new Date();
        }
        catch {
            return false;
        }
    };
    const caOptions = [
        { value: '', label: t('tls.noneSelfSigned') },
        ...certs.filter((c) => c.is_ca && !!c.key_file).map((c) => ({ value: c.id, label: c.name || c.id })),
    ];
    const selection = useSelection(certs.map((c) => c.id));
    const bulkDelete = useCallback(async () => {
        const ok = await confirm({
            title: t('app.bulkDelete'), message: t('tls.deleteConfirm', { id: `${selection.count} certs` }),
            danger: true, confirmText: t('app.delete'), cancelText: t('app.cancel'),
        });
        if (!ok)
            return;
        try {
            await Promise.all([...selection.selected].map((id) => api.deleteCert(id)));
            toast.success(`${t('app.bulkDelete')}: ${selection.count}`);
            queryClient.invalidateQueries({ queryKey: ['certs'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [selection, confirm, queryClient, toast, t]);
    const certColumns = [
        {
            key: 'name', title: t('tls.name'), render: (cert) => (_jsxs(_Fragment, { children: [_jsx("b", { children: cert.name || cert.id }), _jsx("span", { className: "peer-addr-sub", children: cert.id }), isExpired(cert.not_after) && _jsxs(_Fragment, { children: [" ", _jsx(Badge, { variant: "muted", children: t('tls.expired') })] })] })),
        },
        { key: 'subject', title: t('tls.subject'), render: (cert) => cert.subject },
        { key: 'issuer', title: t('tls.issuer'), render: (cert) => _jsxs(_Fragment, { children: [cert.issuer, cert.is_ca && _jsxs(_Fragment, { children: [" ", _jsx(Badge, { variant: "blue", children: "CA" })] })] }) },
        { key: 'expires', title: t('tls.expires'), render: (cert) => _jsx("span", { className: "mono", children: cert.not_after }) },
        { key: 'key', title: t('tls.hasKey'), render: (cert) => cert.key_file ? _jsx(Badge, { variant: "green", children: t('app.yes') }) : _jsx(Badge, { variant: "muted", children: t('app.no') }) },
        {
            key: 'actions', title: '', width: '40px', render: (cert) => (_jsx("button", { className: "hy-row-edit", onClick: (e) => handleEdit(cert, e), title: t('app.edit'), children: _jsxs("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: "2", children: [_jsx("path", { d: "M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7" }), _jsx("path", { d: "M18.5 2.5a2.12 2.12 0 013 3L12 15l-4 1 1-4 9.5-9.5z" })] }) })),
        },
    ];
    return (_jsxs("div", { style: { display: 'flex', flexDirection: 'column', gap: 20 }, children: [_jsx(Card, { title: t('tls.title'), count: certs.length, actions: _jsxs("div", { style: { display: 'flex', gap: 8, alignItems: 'center' }, children: [_jsx(BulkActionBar, { count: selection.count, onClear: selection.clear, children: _jsx(Button, { size: "sm", variant: "danger", onClick: bulkDelete, children: t('app.bulkDelete') }) }), _jsx(Button, { size: "sm", variant: "primary", onClick: openNew, children: t('tls.new') })] }), noPadding: true, children: _jsx(Table, { columns: certColumns, data: certs, rowKey: (c) => c.id, rowClassName: (c) => isExpired(c.not_after) ? 'disabled-row' : undefined, emptyText: t('tls.noCerts'), selection: selection }) }), _jsx(Modal, { open: modalOpen, onClose: () => setModalOpen(false), title: editMode ? t('tls.editTitle') : t('tls.newTitle'), animateFrom: clickPos, footer: _jsxs(_Fragment, { children: [_jsx(Button, { onClick: () => setModalOpen(false), children: t('app.cancel') }), _jsx(Button, { variant: "primary", onClick: handleSave, loading: saving, children: editMode ? t('app.save') : t('tls.new') })] }), children: _jsxs("div", { style: { display: 'flex', flexDirection: 'column', gap: 14 }, children: [_jsxs(FormGrid, { children: [_jsx(FormGroup, { label: t('tls.id'), required: true, children: _jsx(Input, { value: certId, onChange: (e) => setCertId(e.target.value), placeholder: "e.g. my-cert", disabled: editMode }) }), _jsx(FormGroup, { label: t('tls.name'), children: _jsx(Input, { value: certName, onChange: (e) => setCertName(e.target.value), placeholder: t('tls.optional') }) })] }), !editMode && (_jsx(FormGroup, { label: t('tls.signWithCA'), children: _jsx(Select, { value: caId, onChange: (e) => setCaId(e.target.value), options: caOptions }) })), caId && !editMode && (_jsx(FormGroup, { label: t('tls.commonName'), required: true, children: _jsx(Input, { value: cn, onChange: (e) => setCn(e.target.value), placeholder: "e.g. vpn.example.com" }) })), (!caId || editMode) && (_jsxs(_Fragment, { children: [_jsx(Tabs, { items: [
                                        { key: 'paste', label: t('tls.manualInput') },
                                        { key: 'path', label: t('tls.filePath') },
                                    ], activeKey: certTab, onChange: setCertTab, addon: _jsx("button", { className: "hy-circle-btn", title: t('tls.generate'), onClick: handleGenerate, disabled: generating, children: _jsx("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: "2", children: _jsx("path", { d: "M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4" }) }) }) }), _jsx(TabPanel, { activeKey: certTab, keys: ['paste', 'path'], children: certTab === 'paste' ? (_jsxs("div", { style: { display: 'flex', flexDirection: 'column', gap: 14 }, children: [_jsx(FormGroup, { label: t('tls.certPem'), required: true, children: _jsx(Textarea, { value: certPem, onChange: (e) => setCertPem(e.target.value), rows: 5, monospace: true, placeholder: "-----BEGIN CERTIFICATE-----", ...makeDrop(setCertPem) }) }), _jsx(FormGroup, { label: t('tls.keyPem'), children: _jsx(Textarea, { value: keyPem, onChange: (e) => setKeyPem(e.target.value), rows: 4, monospace: true, placeholder: "-----BEGIN EC PRIVATE KEY-----", ...makeDrop(setKeyPem) }) })] })) : (_jsxs(FormGrid, { children: [_jsx(FormGroup, { label: t('tls.certPath'), required: true, children: _jsx(Input, { value: certPath, onChange: (e) => setCertPath(e.target.value), placeholder: "/etc/ssl/cert.pem" }) }), _jsx(FormGroup, { label: t('tls.keyPath'), children: _jsx(Input, { value: keyPath, onChange: (e) => setKeyPath(e.target.value), placeholder: "/etc/ssl/key.pem" }) })] })) })] }))] }) })] }));
}
