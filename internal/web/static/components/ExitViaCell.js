import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { Badge, Tooltip } from '@hy2scale/ui';
import { useExitPaths } from '@/hooks/useExitPaths';
export function ExitViaCell({ exitVia, exitPaths, exitMode }) {
    const { isReachableAt } = useExitPaths();
    if (!exitVia)
        return _jsx("span", { style: { color: 'var(--text-muted)' }, children: "\u2014" });
    const paths = exitPaths && exitPaths.length > 0 ? exitPaths : [exitVia];
    const primary = paths[0];
    const renderPath = (path) => {
        const hops = path.split('/');
        let unreachable = false;
        return (_jsx("span", { style: { fontFamily: 'var(--mono)', fontSize: 12 }, children: hops.map((hop, i) => {
                // Build qualified prefix for this hop position
                const qp = hops.slice(0, i + 1).join('/');
                if (!unreachable && !isReachableAt(qp))
                    unreachable = true;
                const color = unreachable ? 'var(--red)' : 'var(--green)';
                return (_jsxs("span", { children: [i > 0 && _jsx("span", { style: { color: 'var(--text-muted)', margin: '0 2px' }, children: "/" }), _jsx("span", { style: { color, fontWeight: 600 }, children: hop })] }, i));
            }) }));
    };
    const modeBadge = exitMode === 'quality' ? (_jsx(Badge, { variant: "green", className: "ml-1", children: "Q" })) : exitMode === 'aggregate' ? (_jsx(Badge, { variant: "blue", className: "ml-1", children: "A" })) : null;
    const extraCount = paths.length - 1;
    return (_jsxs("span", { style: { display: 'inline-flex', alignItems: 'center', gap: 4 }, children: [renderPath(primary), modeBadge, extraCount > 0 && (_jsx(Tooltip, { content: _jsx("div", { children: paths.map((p, i) => (_jsxs("div", { style: { fontFamily: 'var(--mono)', fontSize: 12 }, children: [i === 0 ? 'primary: ' : `fallback ${i}: `, p] }, i))) }), children: _jsxs(Badge, { variant: "muted", children: ["+", extraCount] }) }))] }));
}
