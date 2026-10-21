#!/usr/bin/env python3
"""Re-read every comparison source and every claim marked ``reverify``, and print the owner's
review table (P6 S-5, G5; D113, D212; standard library only).

    python3 scripts/compare_review.py --fetch                  # read every source page (public GETs)
    python3 scripts/compare_review.py --cache tmp/compare-review   # re-print from saved pages

For each cell of contracts/claims/compare.v1.json the source page is fetched (a plain GET, no key,
no cookie), reduced to its visible text, and searched for the sentence that best supports the
cell's own words. The table on stdout is what the owner reviews at G5: each cell with its value,
our words, the **quoted sentence from the source**, the link, the HTTP status and the date read.
Nothing is judged automatically: a low match score only marks the cell for a closer look.

For each claim in value-claims.v1.json with a ``reverify`` note and a source URL, the page is
checked for the claim's figures (for example "95.5%" or "171 ms"); a figure missing from the page
means the claim cannot be confirmed from its source.

Outputs (paths default under the gitignored tmp/compare-review/):
  --tsv FILE        one row per cell, its current source only: cell, url, http_status, date
  --proposal FILE   the contract edits the result supports, as JSON, for the lead (this script
                    never edits contracts/): an as_of for every cell whose source was read, the
                    cells to drop (no source, or a source that no longer answers), and each
                    reverify claim's state (confirmed, or not found on its source)
  --cache DIR       where fetched pages are kept (one .txt of visible text per URL), and where
                    --cache without --fetch reads them back

Exit status: 0 every source read and every reverify claim confirmed; 1 a source failed or a
reverify claim was not found on its page (the table is printed either way); 2 usage error.
"""

from __future__ import annotations

import argparse
import datetime as _dt
import hashlib
import html
import json
import re
import sys
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, Dict, List, Optional, Sequence, Tuple

ROOT = Path(__file__).resolve().parents[1]
COMPARE_PATH = "contracts/claims/compare.v1.json"
CLAIMS_PATH = "contracts/claims/value-claims.v1.json"
DEFAULT_DIR = "tmp/compare-review"
USER_AGENT = "Mozilla/5.0 (X11; Linux x86_64) TrustAgent-compare-review (+https://github.com/timkush1/TrustAgent)"
WEAK_MATCH = 0.34
STOPWORDS = frozenset(
    "the a an and or of to in on for with by is are be it its as at from that this your you can "
    "not no yes any each per one into than then when which who what where how also only own all "
    "but does do has have was were will may their they there these those via more most".split()
)

Fetcher = Callable[[str], Tuple[str, str]]


@dataclass
class Page:
    url: str
    status: str  # an HTTP status code, or "error: ..." when the page could not be read
    text: str

    @property
    def ok(self) -> bool:
        return self.status == "200"


# ------------------------------------------------------------------------------ text


def visible_text(raw: str) -> str:
    """The words a reader sees: no scripts, styles, tags or entities; whitespace collapsed."""
    text = re.sub(r"(?is)<(script|style|noscript|template)\b[^>]*>.*?</\1\s*>", " ", raw)
    text = re.sub(r"(?is)<!--.*?-->", " ", text)
    text = re.sub(r"(?i)<(br|/p|/div|/li|/h[1-6]|/tr|/td|/th|/dd|/dt)\b[^>]*>", ". ", text)
    text = re.sub(r"(?s)<[^>]+>", " ", text)
    text = html.unescape(text)
    return re.sub(r"\s+", " ", text).strip()


def sentences(text: str) -> List[str]:
    parts = re.split(r"(?<=[.!?])\s+(?=[A-Z0-9\"“(])|\s*\.\s+\.\s*", text)
    return [p.strip(" .") + "." for p in parts if len(p.strip(" .").split()) >= 4]


def words(text: str) -> List[str]:
    found = re.findall(r"[a-z0-9][a-z0-9-]*", text.lower())
    return [w for w in found if len(w) >= 3 and w not in STOPWORDS]


def stem(word: str) -> str:
    for suffix in ("ing", "ed", "es", "s"):
        if len(word) > len(suffix) + 3 and word.endswith(suffix):
            return word[: -len(suffix)]
    return word


def best_sentence(claim: str, text: str) -> Tuple[Optional[str], float]:
    """The page sentence sharing the most of the claim's words (stemmed), and that share (0-1)."""
    want = {stem(w) for w in words(claim)}
    if not want:
        return None, 0.0
    best: Tuple[Optional[str], float] = (None, 0.0)
    for sentence in sentences(text):
        have = {stem(w) for w in words(sentence)}
        score = len(want & have) / len(want)
        if score > best[1] or (score == best[1] and best[0] and len(sentence) < len(best[0])):
            best = (sentence, score)
    return best


def figures(display: str) -> List[str]:
    """The numbers of a display ("95.5%", "171 ms median" -> ["95.5%"], ["171"])."""
    found = re.findall(r"\$?\d[\d,]*(?:\.\d+)?%?", display)
    return [f.rstrip(",") for f in found]


# ------------------------------------------------------------------------------ fetching


def http_fetch(url: str, timeout: float = 30.0) -> Tuple[str, str]:
    """(status, raw page) of a plain GET; status "error: ..." when it fails."""
    if not url.startswith("https://"):
        return "error: not an https URL", ""
    request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT, "Accept": "text/html"})
    try:
        # https only (checked above), a fixed GET with no credentials.
        with urllib.request.urlopen(request, timeout=timeout) as response:  # nosec B310
            body = response.read(5_000_000)
            charset = response.headers.get_content_charset() or "utf-8"
            return str(response.status), body.decode(charset, errors="replace")
    except urllib.error.HTTPError as exc:
        return str(exc.code), ""
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        return f"error: {getattr(exc, 'reason', exc)}", ""


def cache_name(url: str) -> str:
    slug = re.sub(r"[^a-z0-9]+", "-", url.lower().split("://", 1)[-1]).strip("-")[:60]
    return f"{slug}-{hashlib.sha256(url.encode()).hexdigest()[:10]}"


def read_pages(urls: Sequence[str], cache: Path, fetch: Optional[Fetcher]) -> Dict[str, Page]:
    """Each URL's page: fetched (and saved to the cache), or read back from the cache."""
    pages: Dict[str, Page] = {}
    for url in urls:
        base = cache / cache_name(url)
        if fetch is not None:
            status, raw = fetch(url)
            text = visible_text(raw) if raw else ""
            cache.mkdir(parents=True, exist_ok=True)
            base.with_suffix(".txt").write_text(text, encoding="utf-8")
            base.with_suffix(".status").write_text(status, encoding="utf-8")
        else:
            try:
                text = base.with_suffix(".txt").read_text(encoding="utf-8")
                status = base.with_suffix(".status").read_text(encoding="utf-8").strip()
            except OSError:
                text, status = "", "error: not in the cache (run with --fetch)"
        pages[url] = Page(url=url, status=status, text=text)
    return pages


# ------------------------------------------------------------------------------ review


def quote_cell(text: str) -> str:
    return text.replace("|", "\\|").replace("\n", " ")


def review(
    compare: dict, claims: dict, pages: Dict[str, Page], today: str
) -> Tuple[List[str], List[List[str]], dict, bool]:
    """(the Markdown table lines, the TSV rows, the proposal, every check passed)."""
    columns = {c["id"]: c.get("label", c["id"]) for c in compare.get("columns", [])}
    lines = [
        f"## Comparison review (G5), sources read {today}",
        "",
        "| # | Cell | Says | Our words | The source's words (best match) | Match | Source | Read |",
        "|---|---|---|---|---|---|---|---|",
    ]
    tsv = [["cell", "url", "http_status", "date"]]
    proposal: dict = {"as_of": {}, "drop": [], "claims": {}, "weak_match": []}
    passed = True
    n = 0
    for row in compare.get("rows", []):
        for col, cell in row.get("cells", {}).items():
            n += 1
            cell_id = f"{row['id']}.{col}"
            url = cell.get("source_url")
            if not url:
                proposal["drop"].append({"cell": cell_id, "why": "no source"})
                lines.append(
                    f"| {n} | {cell_id} | {cell.get('value')} | {quote_cell(cell.get('text', ''))} | (no source: drop) | | | |"
                )
                passed = False
                continue
            page = pages[url]
            tsv.append([cell_id, url, page.status, today])
            if not page.ok:
                proposal["drop"].append({"cell": cell_id, "why": f"source answered {page.status}"})
                passed = False
                quote, score = "(the page could not be read)", 0.0
            else:
                found, score = best_sentence(cell.get("text", ""), page.text)
                quote = f"“{found}”" if found else "(no sentence on the page)"
                proposal["as_of"][cell_id] = today
                if score < WEAK_MATCH:
                    proposal["weak_match"].append(cell_id)
            label = f"{row.get('name', row['id'])}: {columns.get(col, col)}"
            vendor = cell.get("vendor") or ""
            lines.append(
                f"| {n} | {quote_cell(label)} | {cell.get('value')} · {quote_cell(vendor)} | "
                f"{quote_cell(cell.get('text', ''))} | {quote_cell(quote)} | {score:.0%} | "
                f"[{quote_cell(url.split('://', 1)[-1][:48])}]({url}) | {page.status} |"
            )
    lines += [
        "",
        "Match: the share of our words found in that sentence (a mark for a closer look, not a "
        f"verdict; below {WEAK_MATCH:.0%} the cell is listed under weak_match).",
    ]
    reverify = [c for c in claims.get("claims", []) if c.get("reverify")]
    if reverify:
        lines += [
            "",
            "## Claims to re-verify",
            "",
            "| Claim | Figure | Source | Found on the page |",
            "|---|---|---|---|",
        ]
    for claim in reverify:
        url = claim.get("source_url")
        page = pages.get(url) if url else None
        wanted = figures(claim.get("display", ""))
        if page is None or not page.ok:
            state = "source not read"
            found_all = False
        else:
            missing = [f for f in wanted if f.rstrip("%") not in page.text]
            found_all = bool(wanted) and not missing
            state = (
                "confirmed"
                if found_all
                else f"not found on its source ({', '.join(missing) or 'no figure'})"
            )
        proposal["claims"][claim["id"]] = {
            "state": "confirmed" if found_all else "not_found",
            "source_url": url,
            "figures": wanted,
            "read": today,
        }
        passed = passed and found_all
        lines.append(
            f"| {claim['id']} | {quote_cell(claim.get('display', ''))} | {url} | {state} |"
        )
    return lines, tsv, proposal, passed


def main(
    argv: Optional[Sequence[str]] = None,
    root: Path = ROOT,
    fetcher: Fetcher = http_fetch,
    today: Optional[str] = None,
) -> int:
    parser = argparse.ArgumentParser(
        prog="compare_review.py", description=__doc__.split("\n", 1)[0]
    )
    parser.add_argument(
        "--fetch", action="store_true", help="read every source page now (public GETs)"
    )
    parser.add_argument("--cache", type=Path, help=f"saved pages (default {DEFAULT_DIR}/pages)")
    parser.add_argument(
        "--tsv", type=Path, help=f"the fetch log (default {DEFAULT_DIR}/compare-fetch.tsv)"
    )
    parser.add_argument(
        "--proposal",
        type=Path,
        help=f"proposed contract edits (default {DEFAULT_DIR}/proposal.json)",
    )
    parser.add_argument("--date", help="the read date to record (default: today, UTC)")
    args = parser.parse_args(argv)
    day = args.date or today or _dt.datetime.now(_dt.timezone.utc).date().isoformat()
    try:
        compare = json.loads((root / COMPARE_PATH).read_text(encoding="utf-8"))
        claims = json.loads((root / CLAIMS_PATH).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        print(f"compare-review: {exc}", file=sys.stderr)
        return 2
    out_dir = root / DEFAULT_DIR
    cache = args.cache or out_dir / "pages"
    urls = sorted(
        {
            c["source_url"]
            for r in compare.get("rows", [])
            for c in r.get("cells", {}).values()
            if c.get("source_url")
        }
        | {
            c["source_url"]
            for c in claims.get("claims", [])
            if c.get("reverify") and c.get("source_url")
        }
    )
    pages = read_pages(urls, cache, fetcher if args.fetch else None)
    lines, tsv, proposal, passed = review(compare, claims, pages, day)
    tsv_path = args.tsv or out_dir / "compare-fetch.tsv"
    proposal_path = args.proposal or out_dir / "proposal.json"
    for path in (tsv_path, proposal_path):
        path.parent.mkdir(parents=True, exist_ok=True)
    tsv_path.write_text("\n".join("\t".join(r) for r in tsv) + "\n", encoding="utf-8")
    proposal_path.write_text(json.dumps(proposal, indent=2) + "\n", encoding="utf-8")
    sys.stdout.write("\n".join(lines) + "\n")
    print(
        f"compare-review: {len(tsv) - 1} cells, {len(urls)} pages; log {tsv_path}, proposal {proposal_path}",
        file=sys.stderr,
    )
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
