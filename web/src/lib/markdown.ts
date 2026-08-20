import { Marked } from 'marked';
import { markedHighlight } from 'marked-highlight';
// Core + a curated language set: importing all of highlight.js costs ~900KB
// of bundle for languages that will never appear in a coding session here.
import hljs from 'highlight.js/lib/core';
import bash from 'highlight.js/lib/languages/bash';
import c from 'highlight.js/lib/languages/c';
import cpp from 'highlight.js/lib/languages/cpp';
import css from 'highlight.js/lib/languages/css';
import diff from 'highlight.js/lib/languages/diff';
import dockerfile from 'highlight.js/lib/languages/dockerfile';
import go from 'highlight.js/lib/languages/go';
import java from 'highlight.js/lib/languages/java';
import javascript from 'highlight.js/lib/languages/javascript';
import json from 'highlight.js/lib/languages/json';
import makefile from 'highlight.js/lib/languages/makefile';
import markdown from 'highlight.js/lib/languages/markdown';
import python from 'highlight.js/lib/languages/python';
import rust from 'highlight.js/lib/languages/rust';
import sql from 'highlight.js/lib/languages/sql';
import toml from 'highlight.js/lib/languages/ini';
import typescript from 'highlight.js/lib/languages/typescript';
import xml from 'highlight.js/lib/languages/xml';
import yaml from 'highlight.js/lib/languages/yaml';
import DOMPurify from 'dompurify';

hljs.registerLanguage('bash', bash);
hljs.registerLanguage('c', c);
hljs.registerLanguage('cpp', cpp);
hljs.registerLanguage('css', css);
hljs.registerLanguage('diff', diff);
hljs.registerLanguage('dockerfile', dockerfile);
hljs.registerLanguage('go', go);
hljs.registerLanguage('java', java);
hljs.registerLanguage('javascript', javascript);
hljs.registerLanguage('json', json);
hljs.registerLanguage('makefile', makefile);
hljs.registerLanguage('markdown', markdown);
hljs.registerLanguage('python', python);
hljs.registerLanguage('rust', rust);
hljs.registerLanguage('sql', sql);
hljs.registerLanguage('toml', toml);
hljs.registerLanguage('typescript', typescript);
hljs.registerLanguage('xml', xml);
hljs.registerLanguage('yaml', yaml);

const marked = new Marked(
  markedHighlight({
    langPrefix: 'hljs language-',
    highlight(code, lang) {
      if (lang && hljs.getLanguage(lang)) {
        return hljs.highlight(code, { language: lang }).value;
      }
      // Unfenced/unknown languages stay plain; auto-detection over a
      // trimmed language set misfires more than it helps.
      return code;
    },
  }),
);

// Completed messages get full markdown; streaming text is rendered as
// plain preformatted text and upgraded once the message lands; re-parsing
// the whole document per token is the classic streaming-jank source.
export function renderMarkdown(text: string): string {
  const html = marked.parse(text, { async: false }) as string;
  return linkifyFilePaths(DOMPurify.sanitize(html));
}

// --- Clickable file paths -------------------------------------------------
//
// File paths in model output, thinking, and tool calls become links that
// open the file in the browser panel. The chat thread handles the clicks
// (see Chat.svelte); these helpers only turn path-like tokens into
// anchors carrying the path in data-file.

// Text-ish file extensions: the file browser views raw text, so linking a
// binary (png, zip, ...) would only open garbage. Longest first, so the
// alternation prefers "tsx" over "ts" inside "foo.tsx".
const FILE_EXTENSIONS = [
  'markdown',
  'jsonc',
  'jsonl',
  'properties',
  'editorconfig',
  'tfvars',
  'graphql',
  'dockerfile',
  'tsx',
  'mts',
  'cts',
  'jsx',
  'mjs',
  'cjs',
  'cpp',
  'cxx',
  'hpp',
  'scss',
  'sass',
  'less',
  'bash',
  'zsh',
  'fish',
  'mdx',
  'rst',
  'yml',
  'yaml',
  'toml',
  'html',
  'htm',
  'svelte',
  'vue',
  'swift',
  'java',
  'scala',
  'gradle',
  'proto',
  'prisma',
  'ps1',
  'bat',
  'cmd',
  'diff',
  'patch',
  'json',
  'ts',
  'js',
  'go',
  'py',
  'pyw',
  'rb',
  'rs',
  'kt',
  'kts',
  'cc',
  'hh',
  'cs',
  'fs',
  'php',
  'sh',
  'md',
  'txt',
  'ini',
  'cfg',
  'conf',
  'env',
  'lock',
  'css',
  'xml',
  'svg',
  'sql',
  'log',
  'c',
  'h',
  'tf',
  'hcl',
].join('|');

// Well-known bare file names (no extension, no directory part).
const FILE_BARE_NAMES = [
  'Makefile',
  'Dockerfile',
  'LICENSE',
  'README',
  'CHANGELOG',
  'go\\.mod',
  'go\\.sum',
  'go\\.work',
  '\\.gitignore',
  '\\.gitattributes',
  '\\.dockerignore',
  '\\.editorconfig',
  '\\.env',
].join('|');

const FILE_PATH_RE = new RegExp(
  `(?<![A-Za-z0-9_/~-])(?:` +
    // multi-segment path, optionally prefixed ./ ../ or /, optionally
    // ending in a known extension (so directory paths link too)
    `(?:[.]{1,2}/|/)?(?:[A-Za-z0-9_@.+~-]+/)+[A-Za-z0-9_@.+~-]+(?:\\.(?:${FILE_EXTENSIONS}))?` +
    `|` +
    // bare file name with a known extension ("./foo.ts" keeps its prefix)
    `(?:[.]{1,2}/)?[A-Za-z0-9_@.+~-]+\\.(?:${FILE_EXTENSIONS})` +
    `|` +
    // well-known bare file names
    `(?:${FILE_BARE_NAMES})` +
    `)(?![A-Za-z0-9_/~-])`,
  'g',
);

// Fast path: skip the walk entirely when the text cannot contain a path
// (no slash, no dotted token, no well-known bare name).
const NEEDS_LINKIFY = new RegExp(`[/]|[A-Za-z0-9_@.+~-]+\\.[A-Za-z]{1,10}|(?:${FILE_BARE_NAMES})`);

const escHtml = (s: string) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
const escAttr = (s: string) => escHtml(s).replace(/"/g, '&quot;');

// True when the match sits inside a URL: after a scheme ("https://
// example.com/foo.ts"), after a hostname ("example.com/foo.ts" → the
// match is "com/foo.ts"), or when the match's own first segment looks
// like a hostname ("example.com/foo.ts" — a dot before the first slash
// with no explicit path prefix). None of these are file paths.
function isUrlish(raw: string, start: number, full: string): boolean {
  const scheme = raw.lastIndexOf('://', start - 1);
  if (scheme >= 0 && !/\s/.test(raw.slice(scheme + 3, start))) return true;
  if (start > 0 && raw[start - 1] === '.') {
    const before = raw.slice(0, start - 1);
    const ws = before.search(/\s/);
    const token = ws >= 0 ? before.slice(ws + 1) : before;
    if (/^[A-Za-z0-9][A-Za-z0-9.-]*$/.test(token) && !token.includes('/')) return true;
  }
  if (full.startsWith('.') || full.startsWith('/')) return false;
  // A hostname reads as a path only when it prefixes one:
  // "example.com/foo.ts" is a URL, bare "README.md" is a file.
  const slash = full.indexOf('/');
  if (slash < 0) return false;
  return full.slice(0, slash).includes('.');
}

// Linkifies one already-HTML-escaped text segment. "enabled" is false
// inside an existing <a> (never nest anchors).
function linkifyHtmlText(raw: string, enabled: boolean): string {
  if (!enabled || !NEEDS_LINKIFY.test(raw)) return raw;
  let out = '';
  let i = 0;
  FILE_PATH_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = FILE_PATH_RE.exec(raw))) {
    const full = m[0];
    // A trailing period is sentence punctuation, not part of the path
    // ("Update foo.ts." links foo.ts, period stays outside).
    const trimmed = full.replace(/\.+$/, '');
    const tail = full.slice(trimmed.length);
    // Digits-only tokens like "1/2" or "v1.2.3" are never file paths;
    // neither are tokens inside URLs.
    if (!trimmed || !/[A-Za-z]/.test(trimmed) || isUrlish(raw, m.index, trimmed)) {
      out += raw.slice(i, m.index + full.length);
      i = m.index + full.length;
      continue;
    }
    out += raw.slice(i, m.index);
    out += `<a class="file-link" href="#" data-file="${escAttr(trimmed)}">` + trimmed + `</a>`;
    out += tail;
    i = m.index + full.length;
  }
  out += raw.slice(i);
  return out;
}

// linkifyText turns plain text (tool call args and results) into HTML
// with clickable file paths.
export function linkifyText(text: string): string {
  return linkifyHtmlText(escHtml(text), true);
}

// linkifyFilePaths post-processes rendered markdown HTML: file paths in
// the output become links. Text inside existing anchors is left alone,
// and tags/attributes are never touched (only text segments are scanned).
export function linkifyFilePaths(html: string): string {
  if (!NEEDS_LINKIFY.test(html)) return html;
  let out = '';
  let last = 0;
  let inAnchor = false;
  for (const tag of html.matchAll(/<[^>]*>/g)) {
    const t = tag[0];
    out += linkifyHtmlText(html.slice(last, tag.index), !inAnchor);
    if (/^<a\b/i.test(t)) inAnchor = true;
    else if (/^<\/a\s*>$/i.test(t)) inAnchor = false;
    out += t;
    last = tag.index + t.length;
  }
  out += linkifyHtmlText(html.slice(last), !inAnchor);
  return out;
}

// File-extension → registered hljs language, for highlighting raw file
// content (the file browser) outside a markdown fence.
const extLang: Record<string, string> = {
  sh: 'bash',
  bash: 'bash',
  zsh: 'bash',
  c: 'c',
  h: 'c',
  cpp: 'cpp',
  cc: 'cpp',
  hpp: 'cpp',
  css: 'css',
  diff: 'diff',
  patch: 'diff',
  go: 'go',
  java: 'java',
  js: 'javascript',
  mjs: 'javascript',
  cjs: 'javascript',
  jsx: 'javascript',
  json: 'json',
  md: 'markdown',
  py: 'python',
  rs: 'rust',
  sql: 'sql',
  toml: 'toml',
  ts: 'typescript',
  tsx: 'typescript',
  svelte: 'xml',
  html: 'xml',
  xml: 'xml',
  svg: 'xml',
  yml: 'yaml',
  yaml: 'yaml',
};

// highlightFile returns sanitized highlighted HTML for a file's content,
// or null when the extension has no registered language (render plain).
export function highlightFile(path: string, content: string): string | null {
  const base = path.split('/').pop() ?? '';
  if (base.toLowerCase() === 'dockerfile') {
    return DOMPurify.sanitize(hljs.highlight(content, { language: 'dockerfile' }).value);
  }
  if (base.toLowerCase() === 'makefile') {
    return DOMPurify.sanitize(hljs.highlight(content, { language: 'makefile' }).value);
  }
  const ext = base.includes('.') ? base.split('.').pop()!.toLowerCase() : '';
  const lang = extLang[ext];
  if (!lang) return null;
  return DOMPurify.sanitize(hljs.highlight(content, { language: lang }).value);
}
