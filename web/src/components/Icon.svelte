<script lang="ts">
  // Inline stroke icons, 16px grid, currentColor; no icon font, no
  // dependency. Names are the vocabulary of the app's meta-controls;
  // primary actions keep words.
  let {
    name,
    size = 16,
    class: klass = '',
  }: { name: string; size?: number; class?: string } = $props();

  const paths: Record<string, string> = {
    plus: 'M8 3.25v9.5M3.25 8h9.5',
    close: 'M4 4l8 8M12 4l-8 8',
    trash: 'M3 4.5h10M6.5 4.5V3.25h3V4.5M4.75 4.5l.45 8a1 1 0 001 .95h3.6a1 1 0 001-.95l.45-8',
    panel: 'M2.5 3.5h11a1 1 0 011 1v7a1 1 0 01-1 1h-11a1 1 0 01-1-1v-7a1 1 0 011-1zM9.5 3.5v9',
    'panel-left':
      'M2.5 3.5h11a1 1 0 011 1v7a1 1 0 01-1 1h-11a1 1 0 01-1-1v-7a1 1 0 011-1zM6.5 3.5v9',
    grid: 'M3 3h4v4H3zM9 3h4v4H9zM3 9h4v4H3zM9 9h4v4H9z',
    box: 'M8 1.75L13.75 5v6L8 14.25 2.25 11V5L8 1.75zM2.25 5L8 8.25 13.75 5M8 8.25v6',
    stop: 'M5 5h6v6H5z',
    download: 'M8 2.75v7M5 7l3 3 3-3M3 13.25h10',
    chevron: 'M4.5 6.5L8 10l3.5-3.5',
    back: 'M9.5 4L6 8l3.5 4',
    branch:
      'M4.5 6.25v3.5M4.5 2.25a2 2 0 100 4 2 2 0 000-4zM4.5 9.75a2 2 0 100 4 2 2 0 000-4zM11.5 6.25a2 2 0 100-4 2 2 0 000 4zM11.5 6.25a5 5 0 01-5 5',
    folder: 'M2.25 4.25a1 1 0 011-1h3.1l1.4 1.5h5a1 1 0 011 1v5.75a1 1 0 01-1 1h-9.5a1 1 0 01-1-1z',
    check: 'M3.5 8.5l3 3 6-7',
    pencil:
      'M11.1 2.6a1.2 1.2 0 011.7 0l.6.6a1.2 1.2 0 010 1.7l-7.6 7.6-2.9.7.7-2.9zM9.9 3.8l2.3 2.3',
    copy: 'M6 6a1 1 0 011-1h6a1 1 0 011 1v6a1 1 0 01-1 1H7a1 1 0 01-1-1zM10 5V3a1 1 0 00-1-1H3a1 1 0 00-1 1v6a1 1 0 001 1h2',
    sun: 'M8 5.5A2.5 2.5 0 118 10.5 2.5 2.5 0 018 5.5zM8 1.25v1.5M8 13.25v1.5M1.25 8h1.5M13.25 8h1.5M3.2 3.2l1.1 1.1M11.7 11.7l1.1 1.1M12.8 3.2l-1.1 1.1M4.3 11.7l-1.1 1.1',
    moon: 'M13.25 9.9A5.9 5.9 0 016.1 2.75 5.9 5.9 0 1013.25 9.9z',
    contrast: 'M8 1.75a6.25 6.25 0 100 12.5 6.25 6.25 0 000-12.5zM8 1.75v12.5',
    alert: 'M8 2.25L14.25 13H1.75zM8 6.2v3.2M8 11.6v.4',
    star: 'M8 1.75l1.9 3.85 4.25.62-3.08 3 .73 4.23L8 11.63l-3.34 1.82-.73-4.23-3.08-3 4.25-.62z',
    bell: 'M8 1.75a4.5 4.5 0 00-4.5 4.5c0 3-1.2 4.25-1.5 4.75h12c-.3-.5-1.5-1.75-1.5-4.75a4.5 4.5 0 00-4.5-4.5zM6.25 12.5a1.75 1.75 0 003.5 0',
  };
</script>

<svg
  width={size}
  height={size}
  viewBox="0 0 16 16"
  fill="none"
  stroke="currentColor"
  stroke-width="1.4"
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden="true"
  class={klass}
>
  <path d={paths[name] ?? ''} />
</svg>
