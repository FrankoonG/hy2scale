import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Tabs, TabPanel } from '@hy2scale/ui';
import { useNodeStore } from '@/store/node';
import Hy2Tab from '@/components/proxies/Hy2Tab';
import Socks5Tab from '@/components/proxies/Socks5Tab';
import SSTab from '@/components/proxies/SSTab';
import L2TPTab from '@/components/proxies/L2TPTab';
import IKEv2Tab from '@/components/proxies/IKEv2Tab';
import WireGuardTab from '@/components/proxies/WireGuardTab';
export default function ProxiesPage() {
    const { t } = useTranslation();
    const limited = useNodeStore((s) => s.node?.limited);
    const [tab, setTab] = useState('hy2');
    const tabs = [
        { key: 'hy2', label: t('proxies.hy2') },
        { key: 'socks5', label: t('proxies.socks5') },
        { key: 'ss', label: t('proxies.ss') },
        { key: 'l2tp', label: t('proxies.l2tp'), disabled: false },
        { key: 'ikev2', label: t('proxies.ikev2'), disabled: false },
        { key: 'wg', label: t('proxies.wireguard'), disabled: false },
    ];
    const renderTab = () => {
        switch (tab) {
            case 'hy2': return _jsx(Hy2Tab, {});
            case 'socks5': return _jsx(Socks5Tab, {});
            case 'ss': return _jsx(SSTab, {});
            case 'l2tp': return _jsx(L2TPTab, { limited: limited });
            case 'ikev2': return _jsx(IKEv2Tab, { limited: limited });
            case 'wg': return _jsx(WireGuardTab, { limited: limited });
            default: return null;
        }
    };
    return (_jsxs("div", { style: { display: 'flex', flexDirection: 'column', gap: 20 }, children: [_jsx(Tabs, { items: tabs, activeKey: tab, onChange: setTab }), _jsx(TabPanel, { activeKey: tab, keys: tabs.map(t => t.key), children: renderTab() })] }));
}
