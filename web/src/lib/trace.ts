// buildTrace turns the flat event stream into the hierarchy the trace
// panel renders: turns containing model-call and tool spans with real
// start/end times, so a waterfall can show where the wall clock went.
import type { AgentEvent, Usage } from './types';

export interface TraceSpan {
  kind: 'model' | 'tool' | 'approval' | 'question' | 'error' | 'compact';
  label: string;
  detail: string;
  start: number;
  end: number;
  usage?: Usage | null;
  firstTokenMs?: number;
  args?: string;
  result?: string;
  diff?: string;
  error?: string;
  decision?: string;
  toolId?: string;
  // children hold a subagent's spans, nested under its spawn_agent call.
  children?: TraceSpan[];
  // notes record interceptor activity around this call (snapshots,
  // diagnostics, hooks): policy the trace must not hide.
  notes?: { module: string; text: string; error?: string }[];
}

export interface Turn {
  id: string;
  start: number;
  end: number;
  stopReason: string;
  spans: TraceSpan[];
  input: number;
  output: number;
  cached: number;
  open: boolean; // still running (no turn_end seen)
}

export function buildTrace(events: AgentEvent[]): Turn[] {
  const turns: Turn[] = [];
  const byId = new Map<string, Turn>();
  // Spans opened but not yet closed, keyed by round / tool_id / approval_id.
  const openModel = new Map<string, TraceSpan>();
  const openTool = new Map<string, TraceSpan>();
  const openApproval = new Map<string, TraceSpan>();
  const openQuestion = new Map<string, TraceSpan>();

  // Subagent events carry parent_id (the spawning tool call). They nest
  // one level under that span instead of forming turns of their own.
  const parentEvents = new Map<string, AgentEvent[]>();
  const nestable = events.filter((ev) => {
    if (!ev.parent_id) return true;
    const list = parentEvents.get(ev.parent_id) ?? [];
    list.push({ ...ev, parent_id: undefined });
    parentEvents.set(ev.parent_id, list);
    return false;
  });
  events = nestable;

  const turnFor = (ev: AgentEvent): Turn => {
    const id = ev.turn_id ?? 'earlier';
    let t = byId.get(id);
    if (!t) {
      t = {
        id,
        start: Date.parse(ev.time),
        end: Date.parse(ev.time),
        stopReason: '',
        spans: [],
        input: 0,
        output: 0,
        cached: 0,
        open: true,
      };
      byId.set(id, t);
      turns.push(t);
    }
    t.end = Math.max(t.end, Date.parse(ev.time));
    return t;
  };

  for (const ev of events) {
    const at = Date.parse(ev.time);
    switch (ev.type) {
      case 'turn_start':
        turnFor(ev).start = at;
        break;
      case 'turn_end': {
        const t = turnFor(ev);
        t.stopReason = ev.stop_reason ?? '';
        t.open = false;
        break;
      }
      case 'model_start': {
        const t = turnFor(ev);
        const span: TraceSpan = {
          kind: 'model',
          label: `model call ${(ev.round ?? 0) + 1}`,
          detail: '',
          start: at,
          end: at,
        };
        t.spans.push(span);
        openModel.set(`${t.id}:${ev.round ?? 0}`, span);
        break;
      }
      case 'usage': {
        const t = turnFor(ev);
        const key = `${t.id}:${ev.round ?? 0}`;
        const span = openModel.get(key);
        if (span) {
          span.end = at;
          span.usage = ev.usage;
          span.firstTokenMs = ev.first_token_ms;
          openModel.delete(key);
        }
        if (ev.usage) {
          t.input += ev.usage.prompt_tokens;
          t.output += ev.usage.completion_tokens;
          t.cached += ev.usage.cached_tokens;
        }
        break;
      }
      case 'tool_start': {
        const t = turnFor(ev);
        const span: TraceSpan = {
          kind: 'tool',
          label: ev.tool_name ?? 'tool',
          detail: ev.tool_args ?? '',
          args: ev.tool_args,
          toolId: ev.tool_id,
          start: at,
          end: at,
        };
        t.spans.push(span);
        if (ev.tool_id) openTool.set(ev.tool_id, span);
        break;
      }
      case 'tool_end': {
        turnFor(ev);
        const span = ev.tool_id ? openTool.get(ev.tool_id) : undefined;
        if (span) {
          span.end = at;
          span.result = ev.result;
          span.diff = ev.diff;
          span.error = ev.error;
          if (ev.tool_id) openTool.delete(ev.tool_id);
        }
        break;
      }
      case 'interceptor': {
        turnFor(ev);
        const span = ev.tool_id ? openTool.get(ev.tool_id) : undefined;
        if (span) {
          span.notes = span.notes ?? [];
          span.notes.push({
            module: ev.tool_name ?? '',
            text: ev.text ?? '',
            error: ev.error,
          });
        }
        break;
      }
      case 'approval_request': {
        const t = turnFor(ev);
        const span: TraceSpan = {
          kind: 'approval',
          label: `approval: ${ev.tool_name ?? ''}`,
          detail: ev.tool_args ?? '',
          start: at,
          end: at,
        };
        t.spans.push(span);
        if (ev.approval_id) openApproval.set(ev.approval_id, span);
        break;
      }
      case 'approval_result': {
        turnFor(ev);
        const span = ev.approval_id ? openApproval.get(ev.approval_id) : undefined;
        if (span) {
          span.end = at;
          span.decision = ev.decision;
          if (ev.approval_id) openApproval.delete(ev.approval_id);
        }
        break;
      }
      case 'question_request': {
        const t = turnFor(ev);
        const span: TraceSpan = {
          kind: 'question',
          label: 'question',
          detail: ev.question ?? '',
          start: at,
          end: at,
        };
        t.spans.push(span);
        if (ev.approval_id) openQuestion.set(ev.approval_id, span);
        break;
      }
      case 'question_result': {
        turnFor(ev);
        const span = ev.approval_id ? openQuestion.get(ev.approval_id) : undefined;
        if (span) {
          span.end = at;
          span.decision = ev.answer;
          if (ev.approval_id) openQuestion.delete(ev.approval_id);
        }
        break;
      }
      case 'compact': {
        const t = turnFor(ev);
        t.stopReason = 'compacted';
        t.open = false;
        // A marker, not a spanning bar: compaction has no measured start,
        // and painting it across the turn buried every other segment.
        t.spans.push({
          kind: 'compact',
          label: 'compact',
          detail: '',
          start: at,
          end: at,
          usage: ev.usage,
        });
        if (ev.usage) {
          t.input += ev.usage.prompt_tokens;
          t.output += ev.usage.completion_tokens;
          t.cached += ev.usage.cached_tokens;
        }
        break;
      }
      case 'error': {
        const t = turnFor(ev);
        t.spans.push({
          kind: 'error',
          label: 'error',
          detail: ev.error ?? '',
          error: ev.error,
          start: at,
          end: at,
        });
        break;
      }
      default:
        // message / deltas don't shape the trace
        break;
    }
  }

  // Attach subagent spans: recurse over each parent's event list, then
  // find the span carrying that tool_id anywhere in the tree.
  if (parentEvents.size > 0) {
    const attach = (spans: TraceSpan[]) => {
      for (const span of spans) {
        if (span.toolId && parentEvents.has(span.toolId)) {
          const childTurns = buildTrace(parentEvents.get(span.toolId)!);
          span.children = childTurns.flatMap((t) => t.spans);
        }
        if (span.children) attach(span.children);
      }
    };
    for (const t of turns) attach(t.spans);
  }
  return turns;
}

// Waterfall segments for one turn, as fractions of the turn's duration.
export interface Segment {
  kind: TraceSpan['kind'];
  left: number; // 0..1
  width: number; // 0..1
}

export function waterfall(turn: Turn): Segment[] {
  const total = Math.max(turn.end - turn.start, 1);
  return turn.spans
    .filter((s) => s.end > s.start)
    .map((s) => {
      const left = Math.min(Math.max((s.start - turn.start) / total, 0), 1);
      const width = Math.max((s.end - s.start) / total, 0.005);
      return {
        kind: s.kind,
        left,
        width: Math.min(width, 1 - left), // never overflow the track
      };
    });
}
