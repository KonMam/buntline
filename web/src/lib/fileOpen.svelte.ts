// A click on a file link in the chat asks the side panel to open that
// file in the file browser. The request lives in a tiny module store so
// the chat (which renders the links) can reach the panel (which renders
// the browser) without threading callbacks through App. The counter n
// makes each click a fresh request: re-clicking the same link reopens
// the file.
export interface FileOpenRequest {
  sessionId: string;
  path: string;
  n: number;
}

export const fileOpen = $state<{ request: FileOpenRequest | null }>({ request: null });
let seq = 0;

export function requestFileOpen(sessionId: string, path: string) {
  fileOpen.request = { sessionId, path, n: ++seq };
}
