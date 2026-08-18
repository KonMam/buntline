// Mirrors of the Go types in internal/agent, internal/provider,
// internal/session, and the module APIs.

export interface ToolCall {
  id: string;
  name: string;
  args: string;
}

export interface Message {
  role: 'system' | 'user' | 'assistant' | 'tool';
  content: string;
  thinking?: string;
  tool_calls?: ToolCall[];
  tool_call_id?: string;
  kind?: string;
  images?: string[];
}

export interface Usage {
  prompt_tokens: number;
  completion_tokens: number;
  cached_tokens: number;
}

export type EventType =
  | 'message'
  | 'text_delta'
  | 'thinking_delta'
  | 'tool_start'
  | 'tool_end'
  | 'tool_bg'
  | 'approval_request'
  | 'approval_result'
  | 'question_request'
  | 'question_result'
  | 'model_start'
  | 'usage'
  | 'turn_start'
  | 'turn_end'
  | 'compact'
  | 'interceptor'
  | 'tasks'
  | 'error';

export interface AgentEvent {
  type: EventType;
  time: string;
  turn_id?: string;
  parent_id?: string;
  round?: number;
  message?: Message;
  text?: string;
  tool_id?: string;
  tool_name?: string;
  tool_args?: string;
  result?: string;
  diff?: string;
  duration_ms?: number;
  first_token_ms?: number;
  approval_id?: string;
  decision?: string;
  question?: string;
  options?: string[];
  answer?: string;
  usage?: Usage | null;
  stop_reason?: string;
  error?: string;
  tasks?: TaskItem[];
}

// TaskItem is one entry in the model's task list (mirror of the Go
// agent.TaskItem). Status is pending, in_progress, or completed.
export interface TaskItem {
  content: string;
  status: 'pending' | 'in_progress' | 'completed';
}

// TasksPayload is the tasks module's route response: the folded list
// plus per-status counts.
export interface TasksPayload {
  tasks: TaskItem[];
  counts: Record<'pending' | 'in_progress' | 'completed', number>;
}

export interface SessionMeta {
  id: string;
  title: string;
  model: string;
  profile?: string;
  workdir: string;
  system_prompt?: string;
  mode?: string;
  created_at: string;
  updated_at: string;
  // Live state, present in the session list only: whether a turn is
  // running and which tool the main loop is executing.
  busy?: boolean;
  running_tool?: string;
}

export interface SessionDetail {
  meta: SessionMeta;
  messages: Message[];
  events: AgentEvent[];
  partial?: { text: string; thinking: string };
  system_prompt?: string;
  context_window?: number;
  pending_approval?: AgentEvent | null;
  pending_question?: AgentEvent | null;
}

export interface ServerConfig {
  model: string;
  base_url: string;
  workdir: string;
  version: string;
}

export interface PendingApproval {
  id: string;
  tool_name: string;
  tool_args: string;
}

export interface PendingQuestion {
  id: string;
  question: string;
  options: string[];
}

export interface ModuleStatus {
  id: string;
  name: string;
  description: string;
  enabled: boolean;
}

// CoreStatus is the modules payload: a read-only core section (part of
// the harness itself, never toggleable) plus the toggleable features.
export interface CoreStatus {
  core: ModuleStatus[];
  modules: ModuleStatus[];
}

export interface SearchHit {
  session_id: string;
  title: string;
  workdir: string;
  role: string;
  snippet: string;
}

export interface AgentInfo {
  name: string;
  description: string;
  tools: string;
}

// A spawned subagent as the registry reports it. The id is the
// spawn_agent tool call id, the same id child events carry as parent_id.
export interface SubagentInfo {
  id: string;
  name: string;
  task: string;
  status: 'running' | 'done' | 'failed' | 'interrupted';
  started_at: string;
  ended_at?: string;
  report?: string;
}

export interface MCPPromptInfo {
  name: string; // "<server>/<prompt>"
  description: string;
  arguments: string[];
}

export interface MCPServerInfo {
  name: string;
  transport: string;
  command?: string;
  args?: string[];
  url?: string;
  env_keys?: string[];
  source: 'config' | 'app';
  status: string;
  tools: string[];
}

export interface Profile {
  name: string;
  base_url: string;
  model: string;
  key_missing?: boolean;
  key_ref?: string;
}

// CatalogProvider is one curated provider entry (mirror of
// internal/config.CatalogProvider).
export interface CatalogProvider {
  name: string;
  label: string;
  tag: string;
  base_url: string;
  env?: string;
  key_url?: string;
  local?: boolean;
  key_missing?: boolean;
  available?: boolean;
  models?: CatalogModel[];
}

export interface CatalogModel {
  name: string;
  label?: string;
  context_window?: number;
}

// AppProvider is a provider the user has activated or added (mirror of
// internal/config.AppProvider).
export interface AppProvider {
  name: string;
  base_url: string;
  model?: string;
  env?: string;
  label?: string;
  tag?: string;
  models?: CatalogModel[];
  key_url?: string;
  local?: boolean;
  custom?: boolean;
  default?: boolean;
}

export interface OllamaModel {
  name: string;
  size: number;
  params: string;
  quant: string;
  fit: 'comfortable' | 'tight' | 'too_large' | 'unknown';
}

export interface FsListing {
  path: string;
  parent: string;
  dirs: string[];
}

export interface WorkdirSuggestions {
  recent: string[];
  default: string;
  home: string;
}

// WorktreeBinding is one managed worktree: the repo it was created
// from, its branch, and the session bound to it ("" when none).
export interface WorktreeBinding {
  path: string;
  repo: string;
  branch: string;
  session?: string;
}

export interface SlashCommand {
  name: string;
  description: string;
  body: string;
  /** Skill marks a SKILL.md entry; commands without frontmatter are plain prompts. */
  skill?: boolean;
  /** Turn-scoped approval grant (skills only): tools that skip the gate this turn. */
  allowedTools?: string[];
}

export interface FileEntry {
  name: string;
  dir: boolean;
  size?: number;
  count?: number;
}
