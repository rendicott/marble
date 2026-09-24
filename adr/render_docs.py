#!/usr/bin/env python3
"""Render Marble ADR markdown to static HTML for the ADR web server.

Usage (from repo root or this directory):
  python3 adr/render_docs.py

Writes:
  adr/NNNN-doc.html   — readable HTML for each NNNN-*.md ADR
  adr/index.html      — catalog with links to doc + review + source md

Requires: pip install 'markdown>=3.5'
"""

from __future__ import annotations

import html
import re
import sys
from datetime import datetime, timezone
from pathlib import Path

try:
    import markdown
except ImportError:
    print("error: install markdown first:  pip install 'markdown>=3.5'", file=sys.stderr)
    sys.exit(1)

ADR_DIR = Path(__file__).resolve().parent
ADR_MD_RE = re.compile(r"^(\d{4})-(.+)\.md$")
STATUS_RE = re.compile(
    r"\*\*Status\*\*\s*\|\s*([^\n|]+)",
    re.IGNORECASE,
)


def discover_adrs() -> list[dict]:
    items = []
    for path in sorted(ADR_DIR.glob("*.md")):
        m = ADR_MD_RE.match(path.name)
        if not m:
            continue
        num, slug = m.group(1), m.group(2)
        text = path.read_text(encoding="utf-8")
        title = title_from_md(text, slug)
        status = status_from_md(text)
        review = ADR_DIR / f"{num}-review.html"
        answers = ADR_DIR / f"{num}-answers.json"
        items.append(
            {
                "num": num,
                "slug": slug,
                "path": path,
                "title": title,
                "status": status,
                "doc_name": f"{num}-doc.html",
                "has_review": review.is_file(),
                "has_answers": answers.is_file(),
                "text": text,
            }
        )
    return items


def title_from_md(text: str, fallback: str) -> str:
    for line in text.splitlines():
        line = line.strip()
        if line.startswith("# "):
            t = line[2:].strip()
            # "ADR-0026: Foo" → "Foo"
            t = re.sub(r"^ADR-\d{4}\s*[:—-]\s*", "", t, flags=re.IGNORECASE)
            return t or fallback
    return fallback.replace("-", " ")


def status_from_md(text: str) -> str:
    m = STATUS_RE.search(text)
    if not m:
        return "unknown"
    raw = m.group(1).strip()
    raw = re.sub(r"\*+", "", raw).strip()
    # Keep first clause before parenthetical nuance if very long
    if len(raw) > 48:
        raw = raw.split("(")[0].strip()
    return raw or "unknown"


def status_class(status: str) -> str:
    s = status.lower()
    if "accept" in s or s == "done" or "implement" in s:
        return "accepted"
    if "supersed" in s or "deprecated" in s or "reject" in s:
        return "superseded"
    if "propos" in s or "draft" in s or "review" in s:
        return "proposed"
    return ""


def rewrite_links(body: str, adrs_by_num: dict[str, dict]) -> str:
    """Point same-dir .md hrefs at rendered *-doc.html when we have one."""

    def repl(m: re.Match) -> str:
        href = m.group(1)
        if href.startswith(("http://", "https://", "#", "mailto:")):
            return m.group(0)
        # adr/0026-foo.md or 0026-foo.md
        base = href.split("#", 1)[0]
        frag = ("#" + href.split("#", 1)[1]) if "#" in href else ""
        name = Path(base).name
        mm = ADR_MD_RE.match(name)
        if mm and mm.group(1) in adrs_by_num:
            return f'href="{mm.group(1)}-doc.html{frag}"'
        if name.endswith("-review.html") or name.endswith("-doc.html"):
            return m.group(0)
        return m.group(0)

    return re.sub(r'href="([^"]+)"', repl, body)


def render_markdown(text: str) -> str:
    md = markdown.Markdown(
        extensions=[
            "tables",
            "fenced_code",
            "toc",
            "sane_lists",
            "smarty",
        ]
    )
    return md.convert(text)


def page_shell(
    *,
    title: str,
    status: str,
    nav_links: str,
    body: str,
    foot: str,
) -> str:
    st_class = status_class(status)
    status_chip = (
        f'<span class="status {html.escape(st_class)}">{html.escape(status)}</span>'
        if status
        else ""
    )
    return f"""<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>{html.escape(title)}</title>
  <link rel="stylesheet" href="doc-kit.css" />
</head>
<body>
  <header class="top">
    <div class="brand">
      <span class="mark"></span>
      <span>{html.escape(title)}</span>
      {status_chip}
    </div>
    <nav class="toc">
{nav_links}
    </nav>
  </header>
  <main>
{body}
  </main>
  <footer class="doc-foot">{foot}</footer>
</body>
</html>
"""


def render_doc(item: dict, adrs_by_num: dict[str, dict]) -> str:
    num = item["num"]
    body_html = rewrite_links(render_markdown(item["text"]), adrs_by_num)
    nav = [
        '      <a href="index.html">Index</a>',
        f'      <a href="{html.escape(item["path"].name)}">Markdown</a>',
    ]
    if item["has_review"]:
        nav.append(f'      <a href="{num}-review.html">Review</a>')
    if item["has_answers"]:
        nav.append(f'      <a href="{num}-answers.json">Answers</a>')
    # prev / next
    nums = sorted(adrs_by_num.keys())
    i = nums.index(num)
    if i > 0:
        p = adrs_by_num[nums[i - 1]]
        nav.append(f'      <a href="{p["doc_name"]}">← {p["num"]}</a>')
    if i + 1 < len(nums):
        n = adrs_by_num[nums[i + 1]]
        nav.append(f'      <a href="{n["doc_name"]}">{n["num"]} →</a>')

    return page_shell(
        title=f"Marble · ADR-{num}",
        status=item["status"],
        nav_links="\n".join(nav),
        body=f'    <article class="doc">\n{body_html}\n    </article>',
        foot=(
            f'Rendered from <code>{html.escape(item["path"].name)}</code> · '
            f'<a href="index.html">all ADRs</a> · '
            f'run <code>python3 adr/render_docs.py</code> to refresh'
        ),
    )


def render_index(items: list[dict]) -> str:
    rows = []
    for it in items:
        links = [
            f'<a class="primary" href="{html.escape(it["doc_name"])}">Read</a>',
        ]
        if it["has_review"]:
            links.append(f'<a href="{it["num"]}-review.html">Review</a>')
        links.append(f'<a href="{html.escape(it["path"].name)}">.md</a>')
        if it["has_answers"]:
            links.append(f'<a href="{it["num"]}-answers.json">answers</a>')
        st = it["status"]
        st_cls = status_class(st)
        rows.append(
            f"""    <li>
      <div class="num">ADR-{it["num"]}</div>
      <div class="title">
        <a href="{html.escape(it["doc_name"])}">{html.escape(it["title"])}</a>
        <span class="slug">{html.escape(it["path"].name)}</span>
      </div>
      <div class="links">
        <span class="status {html.escape(st_cls)}">{html.escape(st)}</span>
        {" ".join(links)}
      </div>
    </li>"""
        )

    now = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC")
    body = f"""    <section class="doc" style="padding:1.25rem 1.4rem;margin-bottom:1rem">
      <h1 style="margin:0 0 0.35rem;font-size:1.55rem">Marble ADRs</h1>
      <p class="muted" style="margin:0">
        Architecture Decision Records — readable HTML. Review pages are for Q&amp;A;
        these docs are the full ADR text. Regenerated {html.escape(now)}.
      </p>
    </section>
    <ul class="index-list">
{chr(10).join(rows)}
    </ul>"""

    return page_shell(
        title="Marble · ADR index",
        status="",
        nav_links='      <a href="README.md">README</a>\n      <a href="../README.md">Repo</a>',
        body=body,
        foot='Run <code>python3 adr/render_docs.py</code> after editing ADR markdown.',
    )


def main() -> int:
    items = discover_adrs()
    if not items:
        print("no ADR markdown found in", ADR_DIR, file=sys.stderr)
        return 1
    by_num = {it["num"]: it for it in items}
    for it in items:
        out = ADR_DIR / it["doc_name"]
        out.write_text(render_doc(it, by_num), encoding="utf-8")
        print("wrote", out.name)
    index = ADR_DIR / "index.html"
    index.write_text(render_index(items), encoding="utf-8")
    print("wrote", index.name)
    print(f"done: {len(items)} ADRs")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
