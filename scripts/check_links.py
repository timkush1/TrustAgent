#!/usr/bin/env python3
"""Check the relative links of the published Markdown (CO-21).

    scripts/check_links.py [--root DIR] [FILE ...]

Scans README.md, docs/*.md and docs/launch/*.md (or the files given) for Markdown links and
images, `[text](target)`, and HTML `href`/`src` attributes. A relative target must name a file
or directory in the repository, and a `#fragment` must be a heading of the target Markdown file
(GitHub's anchor rules: lowercase, punctuation dropped, spaces to hyphens, a numeric suffix for
repeats). External links (http, https, mailto) are not fetched. Code spans and fenced code blocks
are skipped. Exit 0 when every link resolves, 1 otherwise. Standard library only.
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path
from typing import Dict, Iterable, List, Optional, Sequence, Set, Tuple
from urllib.parse import unquote

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_GLOBS = ("README.md", "docs/*.md", "docs/launch/*.md")

MD_LINK = re.compile(r"!?\[(?:[^\]\[]|\[[^\]]*\])*\]\(\s*<?([^)\s>]+)>?(?:\s+\"[^\"]*\")?\s*\)")
HTML_ATTR = re.compile(r"""\b(?:href|src|srcset)\s*=\s*["']([^"']+)["']""")
FENCE = re.compile(r"^\s*(```|~~~)")
CODE_SPAN = re.compile(r"`[^`]*`")
HEADING = re.compile(r"^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$")
EXTERNAL = re.compile(r"^(?:[a-z][a-z0-9+.-]*:|//)", re.IGNORECASE)


def slug(text: str) -> str:
    """GitHub's anchor for a heading: markup and punctuation dropped, lowercase, spaces to hyphens."""
    text = re.sub(r"<[^>]+>", "", text)
    text = re.sub(r"!?\[([^\]]*)\]\([^)]*\)", r"\1", text)
    text = text.replace("`", "").strip().lower()
    text = re.sub(r"[^\w\- ]", "", text)
    return text.replace(" ", "-")


def anchors(markdown: str) -> Set[str]:
    """Every heading anchor of a Markdown text, with -1, -2 … for repeats, plus explicit ids."""
    seen: Dict[str, int] = {}
    out: Set[str] = set()
    fenced = False
    for line in markdown.splitlines():
        if FENCE.match(line):
            fenced = not fenced
            continue
        if fenced:
            continue
        for m in re.finditer(r"""\b(?:id|name)\s*=\s*["']([^"']+)["']""", line):
            out.add(m.group(1))
        h = HEADING.match(line)
        if not h:
            continue
        base = slug(h.group(2))
        n = seen.get(base, 0)
        seen[base] = n + 1
        out.add(base if n == 0 else f"{base}-{n}")
    return out


def links(markdown: str) -> Iterable[Tuple[int, str]]:
    """(line number, target) for every link outside code."""
    fenced = False
    for number, line in enumerate(markdown.splitlines(), start=1):
        if FENCE.match(line):
            fenced = not fenced
            continue
        if fenced:
            continue
        line = CODE_SPAN.sub("", line)
        for m in MD_LINK.finditer(line):
            yield number, m.group(1)
        for m in HTML_ATTR.finditer(line):
            # srcset may list "url 2x, url 1x"; each candidate is a link.
            for candidate in m.group(1).split(","):
                target = candidate.strip().split(" ")[0]
                if target:
                    yield number, target


def check_file(path: Path, root: Path, cache: Dict[Path, Set[str]]) -> List[str]:
    """The problems of one Markdown file's links, as "file:line: message"."""
    text = path.read_text(encoding="utf-8")
    rel = path.relative_to(root).as_posix()
    problems: List[str] = []
    for number, target in links(text):
        if EXTERNAL.match(target):
            continue
        file_part, _, fragment = target.partition("#")
        dest = path if not file_part else (path.parent / unquote(file_part)).resolve()
        if file_part:
            try:
                dest.relative_to(root.resolve())
            except ValueError:
                problems.append(f"{rel}:{number}: {target} leaves the repository")
                continue
            if not dest.exists():
                problems.append(f"{rel}:{number}: {target}: no such file")
                continue
        if fragment and dest.suffix.lower() == ".md" and dest.is_file():
            if dest not in cache:
                cache[dest] = anchors(dest.read_text(encoding="utf-8"))
            if unquote(fragment) not in cache[dest]:
                problems.append(f"{rel}:{number}: {target}: no heading #{fragment}")
    return problems


def default_files(root: Path) -> List[Path]:
    files: List[Path] = []
    for pattern in DEFAULT_GLOBS:
        files.extend(sorted(root.glob(pattern)))
    return files


def main(argv: Optional[Sequence[str]] = None, out=sys.stdout, err=sys.stderr) -> int:
    p = argparse.ArgumentParser(description="Check the relative links of the published Markdown.")
    p.add_argument("--root", type=Path, default=ROOT)
    p.add_argument("files", nargs="*", type=Path)
    a = p.parse_args(argv)
    root = a.root.resolve()
    files = [f.resolve() for f in a.files] if a.files else default_files(root)
    cache: Dict[Path, Set[str]] = {}
    problems = [problem for f in files for problem in check_file(f, root, cache)]
    for problem in problems:
        print(problem, file=err)
    print(f"check_links: {len(files)} file(s), {len(problems)} broken link(s)", file=out)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
