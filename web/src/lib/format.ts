export function formatTokens(n: number): string {
  if (n < 1000) return String(n);
  if (n < 100_000) return (n / 1000).toFixed(1).replace(/\.0$/, '') + 'k';
  return Math.round(n / 1000) + 'k';
}

export function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms % 60_000) / 1000);
  return `${m}m${s.toString().padStart(2, '0')}s`;
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n;
  let i = -1;
  do {
    v /= 1024;
    i++;
  } while (v >= 1024 && i < units.length - 1);
  return `${v >= 10 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

export function formatTime(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

// prettyArgs renders a tool-call JSON argument string compactly for chips
// and log rows: single line, long values elided.
export function prettyArgs(raw: string, max = 120): string {
  try {
    const obj = JSON.parse(raw);
    const parts = Object.entries(obj).map(([k, v]) => {
      let s = typeof v === 'string' ? v : JSON.stringify(v);
      s = s.replace(/\s+/g, ' ');
      if (s.length > 60) s = s.slice(0, 57) + '…';
      return `${k}: ${s}`;
    });
    let out = parts.join(', ');
    if (out.length > max) out = out.slice(0, max - 1) + '…';
    return out;
  } catch {
    return raw.length > max ? raw.slice(0, max - 1) + '…' : raw;
  }
}
