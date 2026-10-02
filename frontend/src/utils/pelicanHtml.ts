/**
 * Pelican artwork sanitization for sandboxed iframe rendering, ported from
 * sub2api. Model output is untrusted: we extract the standalone HTML/SVG
 * document and force a strict CSP so it can never reach the network.
 */
const CSP_META =
  '<meta http-equiv="Content-Security-Policy" content="default-src \'none\'; script-src \'unsafe-inline\'; style-src \'unsafe-inline\'; img-src data: blob:; font-src data:; media-src data:; connect-src \'none\'; form-action \'none\';">'

const START_PATTERNS = ['<!doctype html', '<html', '<svg'] as const

function stripCodeFence(text: string): string {
  return text
    .replace(/^\s*```[a-zA-Z]*[ \t]*\r?\n?/, '')
    .replace(/\r?\n?[ \t]*```\s*$/, '')
    .trim()
}

function injectCsp(doc: string): string {
  const head = /<head[^>]*>/i.exec(doc)
  if (head) {
    const at = head.index + head[0].length
    return doc.slice(0, at) + CSP_META + doc.slice(at)
  }
  const html = /<html[^>]*>/i.exec(doc)
  if (html) {
    const at = html.index + html[0].length
    return doc.slice(0, at) + `<head>${CSP_META}</head>` + doc.slice(at)
  }
  return CSP_META + doc
}

/**
 * Extracts renderable HTML from raw model output: strips markdown fences,
 * slices from the first doctype/html/svg tag to the closing </html>/</svg>,
 * and prepends the CSP meta. Bare SVG gets wrapped into a full document.
 * Returns '' when nothing renderable is found.
 */
export function extractPelicanHtml(raw: string | null | undefined): string {
  if (!raw) return ''
  let text = stripCodeFence(raw)
  if (!text) return ''
  const lower = text.toLowerCase()
  const start = START_PATTERNS.map(pattern => lower.indexOf(pattern))
    .filter(index => index >= 0)
    .sort((a, b) => a - b)[0]
  if (start === undefined) return ''
  text = text.slice(start)
  const lower2 = text.toLowerCase()
  const endHtml = lower2.lastIndexOf('</html>')
  const endSvg = lower2.lastIndexOf('</svg>')
  const end = Math.max(endHtml, endSvg)
  if (end >= 0) {
    const endTagLen = end === endHtml ? '</html>'.length : '</svg>'.length
    text = text.slice(0, end + endTagLen)
  }
  if (!text.trim()) return ''
  const isFullDoc = /^<!doctype html|^<html/i.test(text)
  if (isFullDoc) return injectCsp(text)
  return `<!doctype html><html><head>${CSP_META}</head><body style="margin:0;background:#ffffff">${text}</body></html>`
}
