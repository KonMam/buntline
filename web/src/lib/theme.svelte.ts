// Theme preference: system (default) / light / dark, persisted locally.
// The CSS keys off data-theme on <html>; with no attribute the OS decides.
export type Theme = 'system' | 'light' | 'dark';

const KEY = 'tether-theme';

function apply(theme: Theme) {
  if (theme === 'system') {
    delete document.documentElement.dataset.theme;
  } else {
    document.documentElement.dataset.theme = theme;
  }
}

export class ThemeState {
  current = $state<Theme>('system');

  constructor() {
    const saved = localStorage.getItem(KEY);
    if (saved === 'light' || saved === 'dark') this.current = saved;
    apply(this.current);
  }

  cycle() {
    const order: Theme[] = ['system', 'light', 'dark'];
    this.current = order[(order.indexOf(this.current) + 1) % order.length];
    localStorage.setItem(KEY, this.current);
    apply(this.current);
  }
}
