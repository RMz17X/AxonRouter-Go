// Minimal, XSS-safe markdown for assistant replies.
//
// Model output is untrusted: it can be attacker-influenced (the
// Playground exists to test jailbreaks and personas), so it must never
// reach {@html} raw. This renderer escapes the whole text first, then
// rebuilds a small formatting subset (fenced code blocks, inline code,
// bold, italic, headings, line breaks) using only its own tags — the
// result contains no markup that came from the model.

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

function renderInline(escaped: string): string {
  return escaped
    .replace(/`([^`\n]+)`/g, '<code class="pg-code">$1</code>')
    .replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>')
    .replace(/(^|[^*])\*([^*\n]+)\*/g, '$1<em>$2</em>')
}

export function renderMarkdown(source: string): string {
  const escaped = escapeHtml(source)
  const blocks: string[] = []
  // Split out fenced code blocks first so their contents stay verbatim.
  const parts = escaped.split(/```[^\n]*\n?/)
  for (let i = 0; i < parts.length; i++) {
    const part = parts[i]
    if (i % 2 === 1) {
      blocks.push(`<pre class="pg-pre">${part.replace(/\n$/, '')}</pre>`)
      continue
    }
    for (const para of part.split(/\n{2,}/)) {
      const trimmed = para.trim()
      if (!trimmed) continue
      const heading = trimmed.match(/^(#{1,3})\s+(.*)$/s)
      if (heading && !trimmed.includes('\n')) {
        const level = heading[1].length
        blocks.push(`<p class="pg-h${level}">${renderInline(heading[2])}</p>`)
        continue
      }
      const lines = trimmed.split('\n')
      if (lines.every((l) => /^\s*[-*]\s+/.test(l))) {
        blocks.push(
          `<ul class="pg-ul">${lines
            .map((l) => `<li>${renderInline(l.replace(/^\s*[-*]\s+/, ''))}</li>`)
            .join('')}</ul>`
        )
        continue
      }
      if (lines.every((l) => /^\s*\d+[.)]\s+/.test(l))) {
        blocks.push(
          `<ol class="pg-ol">${lines
            .map((l) => `<li>${renderInline(l.replace(/^\s*\d+[.)]\s+/, ''))}</li>`)
            .join('')}</ol>`
        )
        continue
      }
      if (lines.every((l) => /^\s*(&gt;|>)\s?/.test(l))) {
        const inner = lines.map((l) => renderInline(l.replace(/^\s*(&gt;|>)\s?/, '')))
        blocks.push(`<blockquote class="pg-quote">${inner.join('<br>')}</blockquote>`)
        continue
      }
      blocks.push(`<p>${lines.map(renderInline).join('<br>')}</p>`)
    }
  }
  return blocks.join('')
}
