export function fmtBytes(b) {
    if (b < 1024)
        return b + ' B';
    if (b < 1048576)
        return (b / 1024).toFixed(1) + ' KB';
    if (b < 1073741824)
        return (b / 1048576).toFixed(1) + ' MB';
    return (b / 1073741824).toFixed(2) + ' GB';
}
export function fmtRate(b) {
    if (b < 1024)
        return b + ' B/s';
    if (b < 1048576)
        return (b / 1024).toFixed(1) + ' KB/s';
    if (b < 1073741824)
        return (b / 1048576).toFixed(1) + ' MB/s';
    return (b / 1073741824).toFixed(2) + ' GB/s';
}
export function fmtTraffic(tx, rx) {
    return `↑${fmtRate(tx)} ↓${fmtRate(rx)}`;
}
