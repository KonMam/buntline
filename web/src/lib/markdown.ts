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
  return DOMPurify.sanitize(html);
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
