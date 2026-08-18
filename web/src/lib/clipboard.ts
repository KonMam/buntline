// copyText writes to the clipboard via a real selection and the copy
// command. Content-blocker heuristics (uBlock's ClickFix defuse, which
// flags clipboard writes that look like pasteable commands) intercept
// navigator.clipboard.writeText; a user-gesture copy of a selection is
// the one path they leave alone. The async API stays as the fallback.
export async function copyText(text: string): Promise<void> {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.style.position = 'fixed';
  ta.style.top = '-1000px';
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  document.body.removeChild(ta);
  if (!ok) await navigator.clipboard.writeText(text);
}
