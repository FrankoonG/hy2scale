import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useTranslation } from 'react-i18next';
export default function BulkActionBar({ count, onClear, children }) {
    const { t } = useTranslation();
    if (count === 0)
        return null;
    return (_jsxs("div", { className: "hy-bulk-bar", children: [_jsx("span", { className: "hy-bulk-count", children: t('app.selected', { count }) }), children, _jsx("button", { className: "hy-bulk-clear", onClick: onClear, children: "\u00D7" })] }));
}
