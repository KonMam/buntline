import { describe, expect, it, vi } from 'vitest';
import { linkifyFilePaths, linkifyText, renderMarkdown } from './markdown';

// renderMarkdown sanitizes with DOMPurify, which needs a browser window.
// The mock strips script tags so the test can verify sanitize runs before
// linkification.
vi.mock('dompurify', () => ({
  default: {
    sanitize: (html: string) => html.replace(/<script[\s\S]*?<\/script>/gi, ''),
  },
}));

describe('linkifyText', () => {
  it('turns a path in tool args into a link', () => {
    const out = linkifyText('{"path":"web/src/lib/foo.ts","content":"x"}');
    expect(out).toContain(
      '<a class="file-link" href="#" data-file="web/src/lib/foo.ts">web/src/lib/foo.ts</a>',
    );
  });
  it('links multiple paths in one result', () => {
    const out = linkifyText('wrote web/src/a.go and web/src/b.go');
    expect(out.match(/class="file-link"/g)).toHaveLength(2);
  });
  it('leaves sentence punctuation outside the link', () => {
    const out = linkifyText('Updated src/foo.ts. Done.');
    expect(out).toContain('>src/foo.ts</a>. Done.');
  });
  it('escapes the surrounding text', () => {
    const out = linkifyText('a < b && src/foo.ts > c');
    expect(out).toContain('a &lt; b &amp;&amp; ');
    expect(out).toContain('>src/foo.ts</a>');
  });
  it('keeps ./ and ../ prefixes in the link', () => {
    expect(linkifyText('see ./lib/util.go')).toContain('data-file="./lib/util.go"');
    expect(linkifyText('see ../shared/api.go')).toContain('data-file="../shared/api.go"');
  });
  it('links bare file names with known extensions', () => {
    expect(linkifyText('edit README.md')).toContain('>README.md</a>');
    expect(linkifyText('edit package.json')).toContain('>package.json</a>');
  });
  it('links well-known bare names', () => {
    expect(linkifyText('update Makefile')).toContain('>Makefile</a>');
    expect(linkifyText('add a .gitignore')).toContain('>.gitignore</a>');
  });
  it('does not link version strings, ratios, or prose dots', () => {
    expect(linkifyText('v1.2.3 is out')).not.toContain('file-link');
    expect(linkifyText('a 1/2 chance')).not.toContain('file-link');
    expect(linkifyText('i.e. and e.g.')).not.toContain('file-link');
    expect(linkifyText('it took 0.5s')).not.toContain('file-link');
  });
  it('does not link inside URLs', () => {
    expect(linkifyText('see https://example.com/foo.ts')).not.toContain('file-link');
    expect(linkifyText('see http://x.io/a/b.ts now')).not.toContain('file-link');
    expect(linkifyText('host example.com/foo.ts')).not.toContain('file-link');
  });
  it('does not link plain words with unknown extensions', () => {
    expect(linkifyText('some.thing')).not.toContain('file-link');
  });
});

describe('linkifyFilePaths', () => {
  it('links paths inside markdown code spans', () => {
    const html = '<p><code>src/foo.ts</code></p>';
    const out = linkifyFilePaths(html);
    expect(out).toContain('<a class="file-link" href="#" data-file="src/foo.ts">');
  });
  it('links paths inside highlighted code', () => {
    const html =
      '<pre><code class="hljs"><span class="hljs-string">"src/foo.ts"</span></code></pre>';
    const out = linkifyFilePaths(html);
    expect(out).toContain('data-file="src/foo.ts"');
  });
  it('does not nest anchors inside existing links', () => {
    const html = '<p><a href="https://example.com">src/foo.ts</a></p>';
    const out = linkifyFilePaths(html);
    expect(out).not.toContain('file-link');
    expect(out).toContain('<a href="https://example.com">src/foo.ts</a>');
  });
  it('never touches tag attributes', () => {
    const html = '<p class="src/foo.ts">text</p>';
    const out = linkifyFilePaths(html);
    expect(out).toBe(html);
  });
  it('does not double-escape entities', () => {
    const html = '<p>a &amp; b with src/foo.ts</p>';
    const out = linkifyFilePaths(html);
    expect(out).toContain('a &amp; b with ');
    expect(out).not.toContain('&amp;amp;');
  });
  it('skips content with no path-like tokens', () => {
    const html = '<p>hello world</p>';
    expect(linkifyFilePaths(html)).toBe(html);
  });
});

describe('renderMarkdown', () => {
  it('makes backticked paths clickable end to end', () => {
    const out = renderMarkdown('I updated `web/src/lib/markdown.ts` to linkify paths.');
    expect(out).toContain('data-file="web/src/lib/markdown.ts"');
  });
  it('keeps plain links untouched', () => {
    const out = renderMarkdown('[docs](https://example.com) and `src/main.go`');
    expect(out).toContain('<a href="https://example.com">docs</a>');
    expect(out).toContain('data-file="src/main.go"');
  });
  it('sanitizes before linkifying', () => {
    const out = renderMarkdown('<script>alert(1)</script> src/evil.ts');
    expect(out).not.toContain('<script>');
    expect(out).toContain('data-file="src/evil.ts"');
  });
});
