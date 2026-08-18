import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

export default defineConfig({
  plugins: [svelte()],
  server: {
    proxy: {
      // Dev: Vite serves the frontend, Go serves the API.
      '/api': 'http://localhost:7433',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
});
