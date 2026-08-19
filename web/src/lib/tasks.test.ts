import { describe, expect, it } from 'vitest';
import { foldTasks } from './tasks';
import type { AgentEvent, TaskItem } from './types';

const at = (s: number) => new Date(1_700_000_000_000 + s * 1000).toISOString();

const task = (content: string, status: TaskItem['status']): TaskItem => ({ content, status });

function tasksEvent(time: number, tasks: TaskItem[]): AgentEvent {
  return { type: 'tasks', time: at(time), tasks };
}

describe('foldTasks', () => {
  it('returns an empty list with no tasks events', () => {
    expect(foldTasks([{ type: 'turn_start', time: at(0) }])).toEqual([]);
  });

  it('folds the latest tasks event, last-write-wins', () => {
    const events = [
      tasksEvent(0, [task('a', 'pending')]),
      { type: 'turn_end', time: at(1) } as AgentEvent,
      tasksEvent(2, [task('a', 'completed'), task('b', 'in_progress')]),
    ];
    expect(foldTasks(events)).toEqual([task('a', 'completed'), task('b', 'in_progress')]);
  });

  it('clears on an empty whole-list replace', () => {
    const events = [tasksEvent(0, [task('a', 'pending')]), tasksEvent(1, [])];
    expect(foldTasks(events)).toEqual([]);
  });

  it('clears on a tasks event with no tasks field', () => {
    // The Go bridge writes an empty list as a tasks event without the
    // field (omitempty drops it), so a bare tasks event means cleared.
    const events = [
      tasksEvent(0, [task('a', 'completed'), task('b', 'completed')]),
      { type: 'tasks', time: at(1) } as AgentEvent,
    ];
    expect(foldTasks(events)).toEqual([]);
  });

  it('clears on a tasks event with a non-array payload', () => {
    const events = [
      tasksEvent(0, [task('a', 'pending')]),
      { type: 'tasks', time: at(1), tasks: 'nope' } as unknown as AgentEvent,
    ];
    expect(foldTasks(events)).toEqual([]);
  });

  it('ignores non-tasks events', () => {
    const events = [
      tasksEvent(0, [task('a', 'pending')]),
      { type: 'turn_end', time: at(1) } as AgentEvent,
    ];
    expect(foldTasks(events)).toEqual([task('a', 'pending')]);
  });
});
