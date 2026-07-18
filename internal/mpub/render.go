package mpub

import (
	"fmt"
	"html"
	"strings"
)

// RenderHTMLPage wraps body HTML in a simple Marble shell.
func RenderHTMLPage(title, bodyHTML string) string {
	t := html.EscapeString(title)
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1"/>
  <title>%s · mpub</title>
  <style>
    :root { --bg:#0f1115; --text:#e8ecf4; --muted:#9aa3b5; --accent:#7c9cff; --panel:#171a21; --border:#2a3142; }
    * { box-sizing: border-box; }
    body { margin:0; font-family: "Segoe UI", system-ui, sans-serif; background: var(--bg); color: var(--text); line-height: 1.55; }
    header { border-bottom: 1px solid var(--border); padding: 0.75rem 1.25rem; display:flex; gap:1rem; align-items:center; background:#12151c; }
    header a { color: var(--muted); text-decoration:none; font-size:0.85rem; }
    header a:hover { color: var(--accent); }
    header .title { font-weight:600; }
    main { max-width: 820px; margin: 0 auto; padding: 1.5rem 1.25rem 3rem; }
    article h1, article h2, article h3 { color: #c7d6ff; }
    article pre, article code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.88em; }
    article pre { background:#0c0e13; border:1px solid var(--border); border-radius:10px; padding:0.85rem 1rem; overflow-x:auto; }
    article code { background:#252b3a; padding:0.1rem 0.35rem; border-radius:5px; }
    article pre code { background:transparent; padding:0; }
    article a { color: var(--accent); }
    article p { margin: 0.75rem 0; }
    article ul, article ol { padding-left: 1.35rem; }
    article blockquote { margin: 0.75rem 0; padding: 0.35rem 0.85rem; border-left: 3px solid var(--accent); color: var(--muted); }
    footer { max-width:820px; margin:0 auto; padding:0 1.25rem 2rem; font-size:0.75rem; color:var(--muted); }
  </style>
</head>
<body>
  <header>
    <a href="/mpub">← mpub</a>
    <span class="title">%s</span>
  </header>
  <main><article>
%s
  </article></main>
  <footer>Published via Marble mpub</footer>
</body>
</html>
`, t, t, bodyHTML)
}

// MarkdownToHTML is a small zero-dep markdown subset renderer (good enough for research notes).
func MarkdownToHTML(md string) string {
	lines := strings.Split(md, "\n")
	var b strings.Builder
	inCode := false
	inList := false
	flushList := func() {
		if inList {
			b.WriteString("</ul>\n")
			inList = false
		}
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") {
			flushList()
			if !inCode {
				inCode = true
				b.WriteString("<pre><code>")
			} else {
				inCode = false
				b.WriteString("</code></pre>\n")
			}
			continue
		}
		if inCode {
			b.WriteString(html.EscapeString(line))
			b.WriteByte('\n')
			continue
		}
		if trim == "" {
			flushList()
			continue
		}
		if strings.HasPrefix(trim, "### ") {
			flushList()
			b.WriteString("<h3>")
			b.WriteString(inlineMD(trim[4:]))
			b.WriteString("</h3>\n")
			continue
		}
		if strings.HasPrefix(trim, "## ") {
			flushList()
			b.WriteString("<h2>")
			b.WriteString(inlineMD(trim[3:]))
			b.WriteString("</h2>\n")
			continue
		}
		if strings.HasPrefix(trim, "# ") {
			flushList()
			b.WriteString("<h1>")
			b.WriteString(inlineMD(trim[2:]))
			b.WriteString("</h1>\n")
			continue
		}
		if strings.HasPrefix(trim, "> ") {
			flushList()
			b.WriteString("<blockquote>")
			b.WriteString(inlineMD(trim[2:]))
			b.WriteString("</blockquote>\n")
			continue
		}
		if strings.HasPrefix(trim, "- ") || strings.HasPrefix(trim, "* ") {
			if !inList {
				b.WriteString("<ul>\n")
				inList = true
			}
			b.WriteString("<li>")
			b.WriteString(inlineMD(trim[2:]))
			b.WriteString("</li>\n")
			continue
		}
		flushList()
		b.WriteString("<p>")
		b.WriteString(inlineMD(trim))
		b.WriteString("</p>\n")
	}
	flushList()
	if inCode {
		b.WriteString("</code></pre>\n")
	}
	return b.String()
}

func inlineMD(s string) string {
	// escape first, then apply light markup
	s = html.EscapeString(s)
	// `code`
	s = replaceDelim(s, "`", "<code>", "</code>")
	// **bold**
	s = replaceDelim(s, "**", "<strong>", "</strong>")
	// *em* (simple)
	s = replaceDelim(s, "*", "<em>", "</em>")
	// [text](url)
	s = linkify(s)
	return s
}

func replaceDelim(s, delim, open, close string) string {
	parts := strings.Split(s, delim)
	if len(parts) < 3 {
		return s
	}
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 && i+1 < len(parts) {
			b.WriteString(open)
			b.WriteString(p)
			b.WriteString(close)
		} else {
			b.WriteString(p)
		}
	}
	return b.String()
}

func linkify(s string) string {
	// very small [label](url) — already escaped so look for literal pattern
	// after escape, [ ] ( ) remain
	var out strings.Builder
	for {
		i := strings.Index(s, "[")
		if i < 0 {
			out.WriteString(s)
			break
		}
		j := strings.Index(s[i:], "](")
		if j < 0 {
			out.WriteString(s)
			break
		}
		j += i
		k := strings.Index(s[j+2:], ")")
		if k < 0 {
			out.WriteString(s)
			break
		}
		k += j + 2
		label := s[i+1 : j]
		url := s[j+2 : k]
		out.WriteString(s[:i])
		out.WriteString(`<a href="`)
		out.WriteString(url)
		out.WriteString(`">`)
		out.WriteString(label)
		out.WriteString(`</a>`)
		s = s[k+1:]
	}
	return out.String()
}

// ServeBody returns Content-Type and bytes for HTTP response (document view).
func ServeBody(doc *Doc) (contentType string, body []byte) {
	switch normalizeContentType(doc.Meta.ContentType) {
	case "text/html":
		// if content looks like a full document, pass through
		c := strings.TrimSpace(doc.Content)
		if strings.HasPrefix(strings.ToLower(c), "<!doctype") || strings.HasPrefix(strings.ToLower(c), "<html") {
			return "text/html; charset=utf-8", []byte(doc.Content)
		}
		page := RenderHTMLPage(doc.Meta.Title, doc.Content)
		return "text/html; charset=utf-8", []byte(page)
	case "text/markdown":
		inner := MarkdownToHTML(doc.Content)
		page := RenderHTMLPage(doc.Meta.Title, inner)
		return "text/html; charset=utf-8", []byte(page)
	default:
		// plain as preformatted HTML for readable browser view
		inner := "<pre>" + html.EscapeString(doc.Content) + "</pre>"
		page := RenderHTMLPage(doc.Meta.Title, inner)
		return "text/html; charset=utf-8", []byte(page)
	}
}

// IndexHTML builds the /mpub listing page.
func IndexHTML(docs []Meta) string {
	var items strings.Builder
	if len(docs) == 0 {
		items.WriteString(`<p class="empty">No publications yet. Agents use <code>mpub_publish</code> to create pages here.</p>`)
	} else {
		items.WriteString("<ul class=\"list\">\n")
		for _, d := range docs {
			title := d.Title
			if title == "" {
				title = d.Slug
			}
			items.WriteString(fmt.Sprintf(
				`<li><a href="/mpub/%s"><strong>%s</strong></a> <span class="meta"><code>%s</code> · %s · %s</span></li>`+"\n",
				html.EscapeString(d.Slug),
				html.EscapeString(title),
				html.EscapeString(d.Slug),
				html.EscapeString(d.ContentType),
				html.EscapeString(d.UpdatedAt),
			))
		}
		items.WriteString("</ul>\n")
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1"/>
  <title>mpub · Marble</title>
  <style>
    :root { --bg:#0f1115; --text:#e8ecf4; --muted:#9aa3b5; --accent:#7c9cff; --border:#2a3142; }
    body { margin:0; font-family:"Segoe UI",system-ui,sans-serif; background:var(--bg); color:var(--text); line-height:1.55; }
    header { border-bottom:1px solid var(--border); padding:0.85rem 1.25rem; background:#12151c; }
    header a { color:var(--muted); text-decoration:none; font-size:0.85rem; margin-right:1rem; }
    header a:hover { color:var(--accent); }
    main { max-width:820px; margin:0 auto; padding:1.5rem 1.25rem; }
    h1 { font-size:1.35rem; margin:0 0 0.5rem; }
    .sub { color:var(--muted); font-size:0.9rem; margin-bottom:1.25rem; }
    .list { list-style:none; padding:0; margin:0; }
    .list li { border:1px solid var(--border); border-radius:10px; padding:0.75rem 0.9rem; margin-bottom:0.5rem; background:#171a21; }
    .list a { color:var(--accent); text-decoration:none; }
    .list a:hover { text-decoration:underline; }
    .meta { display:block; margin-top:0.25rem; font-size:0.78rem; color:var(--muted); font-family:ui-monospace,monospace; }
    .empty { color:var(--muted); }
    code { background:#252b3a; padding:0.1rem 0.35rem; border-radius:5px; font-size:0.88em; }
  </style>
</head>
<body>
  <header><a href="/">← Marble</a><span>mpub</span></header>
  <main>
    <h1>Published documents</h1>
    <p class="sub">Agent-published pages under this harness memory.</p>
    %s
  </main>
</body>
</html>
`, items.String())
}
