import type {
  AgentInfo,
  AppProvider,
  CatalogProvider,
  CoreStatus,
  FileEntry,
  FsListing,
  MCPPromptInfo,
  MCPServerInfo,
  SearchHit,
  MemoryOverview,
  MemoryTopic,
  OllamaModel,
  Profile,
  ServerConfig,
  SessionDetail,
  SessionMeta,
  SlashCommand,
  SubagentInfo,
  TasksPayload,
  WorkdirSuggestions,
  WorktreeBinding,
} from './types';

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init);
  if (!res.ok) {
    let detail = res.statusText;
    try {
      const body = await res.json();
      if (body.error) detail = body.error;
    } catch {
      // non-JSON error body; keep statusText
    }
    throw new Error(detail);
  }
  if (res.status === 202 || res.status === 204) return undefined as T;
  return res.json();
}

function post(body: unknown): RequestInit {
  return {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  };
}

export const api = {
  config: () => request<ServerConfig>('/api/config'),

  listSessions: () => request<SessionMeta[]>('/api/sessions'),
  createSession: (workdir?: string, profile?: string, worktree?: string, worktreeName?: string) =>
    request<SessionMeta>(
      '/api/sessions',
      post({ workdir, profile, worktree, worktree_name: worktreeName }),
    ),
  getSession: (id: string) => request<SessionDetail>(`/api/sessions/${id}`),
  deleteSession: (id: string) => request<void>(`/api/sessions/${id}`, { method: 'DELETE' }),
  sendMessage: (id: string, content: string, images?: string[], attachments?: string[]) =>
    request<{ queued?: boolean }>(
      `/api/sessions/${id}/messages`,
      post({ content, images, attachments }),
    ),
  compact: (id: string) => request<void>(`/api/sessions/${id}/compact`, { method: 'POST' }),
  fork: (id: string, keep: number) =>
    request<SessionMeta>(`/api/sessions/${id}/fork`, post({ keep })),
  interrupt: (id: string) => request<void>(`/api/sessions/${id}/interrupt`, { method: 'POST' }),
  getSystemPrompt: () =>
    request<{ prompt: string; overridden: boolean; path: string }>('/api/system-prompt'),
  setSystemPrompt: (prompt: string) =>
    request<{ prompt: string; overridden: boolean }>('/api/system-prompt', {
      ...post({ prompt }),
      method: 'PUT',
    }),
  setMode: (id: string, mode: string) =>
    request<SessionMeta>(`/api/sessions/${id}/mode`, post({ mode })),
  setModel: (
    id: string,
    model: string,
    profile?: string,
  ): Promise<SessionMeta & { context_window?: number }> =>
    request<SessionMeta & { context_window?: number }>(
      `/api/sessions/${id}/model`,
      post({ model, profile }),
    ),
  decide: (approvalId: string, decision: string) =>
    request<void>(`/api/approvals/${approvalId}`, post({ decision })),
  answerQuestion: (questionId: string, answer: string) =>
    request<void>(`/api/questions/${questionId}`, post({ answer })),

  browse: (path?: string) =>
    request<FsListing>(`/api/fs${path ? `?path=${encodeURIComponent(path)}` : ''}`),
  workdirs: () => request<WorkdirSuggestions>('/api/workdirs'),
  profiles: () => request<Profile[]>('/api/profiles'),

  // Provider catalog and app-managed providers (the Models view).
  providers: () => request<CatalogProvider[]>('/api/providers'),
  detectProvider: (key: string) =>
    request<{ provider: string }>('/api/providers/detect', post({ key })),
  providerModels: (name: string) =>
    request<string[]>(`/api/providers/${encodeURIComponent(name)}/models`),
  appProviders: () => request<AppProvider[]>('/api/providers/app'),
  putAppProvider: (p: AppProvider) =>
    request<AppProvider[]>('/api/providers/app', { ...post(p), method: 'PUT' }),
  setAppDefault: (name: string, model: string, isDefault: boolean) =>
    request<AppProvider[]>(`/api/providers/app/${encodeURIComponent(name)}/default`, {
      ...post({ model, default: isDefault }),
      method: 'PUT',
    }),
  deleteAppProvider: (name: string, model?: string) =>
    request<AppProvider[]>(
      `/api/providers/app/${encodeURIComponent(name)}${model ? `?model=${encodeURIComponent(model)}` : ''}`,
      { method: 'DELETE' },
    ),

  // Worktree isolation: create an isolated worktree (and, via
  // createSession's worktree field, a session bound to it) so parallel
  // sessions in one repository never collide.
  worktreeCreate: (repo: string, name?: string) =>
    request<{ path: string; branch: string }>('/api/m/worktrees/', post({ repo, name })),
  worktreeList: (repo: string) =>
    request<{ worktrees: WorktreeBinding[] }>(`/api/m/worktrees/?repo=${encodeURIComponent(repo)}`),
  worktreeDelete: (path: string) =>
    request<void>('/api/m/worktrees/', { ...post({ path }), method: 'DELETE' }),
  secrets: () => request<{ names: string[]; backend: string }>('/api/secrets'),
  setSecret: (name: string, value: string) =>
    request<void>('/api/secrets', { ...post({ name, value }), method: 'PUT' }),
  deleteSecret: (name: string) =>
    request<void>(`/api/secrets/${encodeURIComponent(name)}`, { method: 'DELETE' }),

  modules: () => request<CoreStatus>('/api/modules'),
  setModule: (id: string, enabled: boolean) =>
    request<CoreStatus>(`/api/modules/${id}`, post({ enabled })),

  search: (q: string) => request<{ hits: SearchHit[] }>(`/api/search?q=${encodeURIComponent(q)}`),
  sessionAgents: (id: string) => request<{ agents: AgentInfo[] }>(`/api/sessions/${id}/agents`),
  subagents: (id: string) => request<SubagentInfo[]>(`/api/sessions/${id}/subagents`),
  subagentSteer: (id: string, sid: string, content: string) =>
    request<void>(`/api/sessions/${id}/subagents/${sid}/steer`, post({ content })),
  subagentInterrupt: (id: string, sid: string) =>
    request<void>(`/api/sessions/${id}/subagents/${sid}/interrupt`, { method: 'POST' }),

  mcpServers: () => request<{ servers: MCPServerInfo[] }>('/api/m/mcp/servers'),
  mcpPrompts: () => request<{ prompts: MCPPromptInfo[] }>('/api/m/mcp/prompts'),
  mcpRenderPrompt: (name: string, argument: string) =>
    request<{ text: string }>('/api/m/mcp/prompts/render', post({ name, argument })),
  mcpAddServer: (srv: {
    name: string;
    transport: string;
    command?: string;
    args?: string[];
    url?: string;
    env?: Record<string, string>;
  }) => request<{ servers: MCPServerInfo[] }>('/api/m/mcp/servers', post(srv)),
  mcpRemoveServer: (name: string) =>
    request<{ servers: MCPServerInfo[] }>(`/api/m/mcp/servers/${encodeURIComponent(name)}`, {
      method: 'DELETE',
    }),
  mcpReconnect: (name: string) =>
    request<{ servers: MCPServerInfo[] }>(
      `/api/m/mcp/servers/${encodeURIComponent(name)}/reconnect`,
      { method: 'POST' },
    ),

  filesTree: (session: string, path: string) =>
    request<{ entries: FileEntry[] }>(
      `/api/m/files/tree?session=${session}&path=${encodeURIComponent(path)}`,
    ),
  filesList: (session: string) =>
    request<{ files: string[]; truncated: boolean }>(`/api/m/files/list?session=${session}`),
  filesRead: (session: string, path: string) =>
    request<{ content: string; truncated: boolean }>(
      `/api/m/files/file?session=${session}&path=${encodeURIComponent(path)}`,
    ),

  ollamaModels: () => request<{ models: OllamaModel[]; total_ram: number }>('/api/m/ollama/models'),
  ollamaDelete: (name: string) =>
    request<void>('/api/m/ollama/models', { ...post({ name }), method: 'DELETE' }),
  ollamaPs: () => request<{ models: { name: string }[] }>('/api/m/ollama/ps'),
  ollamaContext: (model: string) =>
    request<{ context_length: number }>(`/api/m/ollama/context?model=${encodeURIComponent(model)}`),

  commandsList: (session: string) =>
    request<{ commands: SlashCommand[] }>(`/api/m/commands/list?session=${session}`),
  commandRender: (session: string, name: string, args: string) =>
    request<{ body: string }>('/api/m/commands/render', post({ session, name, args })),
  memoryOverview: (workdir: string) =>
    request<MemoryOverview>(`/api/m/memory/overview?workdir=${encodeURIComponent(workdir)}`),
  memoryTopic: (workdir: string, name: string) =>
    request<MemoryTopic>(
      `/api/m/memory/topic?workdir=${encodeURIComponent(workdir)}&name=${encodeURIComponent(name)}`,
    ),

  gitStatus: (session: string) =>
    request<{
      repo: boolean;
      branch?: string;
      changed?: number;
      additions?: number;
      deletions?: number;
      files?: { path: string; additions: number; deletions: number; new?: boolean }[];
    }>(`/api/m/git/status?session=${session}`),
  gitCommit: (session: string, message: string) =>
    request<void>('/api/m/git/commit', post({ session, message })),

  checkpointRefs: (session: string) =>
    request<{ refs: Record<string, string> }>(`/api/m/checkpoints/list?session=${session}`),
  checkpointRestore: (session: string, ref: string) =>
    request<void>('/api/m/checkpoints/restore', post({ session, ref })),

  tasks: (session: string) =>
    request<TasksPayload>(`/api/m/tasks/list?session=${encodeURIComponent(session)}`),

  exportURL: (session: string) => `/api/sessions/${session}/export`,

  // Pull streams SSE progress; onProgress receives each raw event.
  async ollamaPull(name: string, onProgress: (p: Record<string, unknown>) => void): Promise<void> {
    const res = await fetch('/api/m/ollama/pull', post({ name }));
    if (!res.ok || !res.body) throw new Error(`pull failed: ${res.statusText}`);
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buf = '';
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      for (;;) {
        const sep = buf.indexOf('\n\n');
        if (sep < 0) break;
        const frame = buf.slice(0, sep);
        buf = buf.slice(sep + 2);
        const data = frame
          .split('\n')
          .filter((l) => l.startsWith('data:'))
          .map((l) => l.slice(5).trim())
          .join('\n');
        if (data) onProgress(JSON.parse(data));
      }
    }
  },
};
