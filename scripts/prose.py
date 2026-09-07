#!/usr/bin/env python3
"""Scan a repository's prose for the patterns docs/style.md lists as tells.

Usage: scripts/prose.py [ROOT]   (default: the current directory)

It reads Markdown files and the visible text of HTML files under docs/,
skips code blocks, tables, diagrams, CHANGELOG.md and style.md, and prints
one line per hit: path:line: CODE: excerpt. Exit status 1 when it found
anything. It sees words and shapes only; read for rhythm, invention and
inflation yourself.
"""
import glob, html, os, re, sys

ROOT = sys.argv[1] if len(sys.argv) > 1 else "."
SKIP = ("CHANGELOG.md", "style.md", "CLAUDE.md", "/data/", "/node_modules/", "/vendor/", "/drafts/")

VOCAB = r"\b(delve|delves|delving|tapestry|testament|pivotal|crucial|robust|comprehensive|fundamentally|nuanced|paradigm|landscape|leverage|leverages|leveraging|seamless|seamlessly|showcase|showcases|showcasing|underscore|underscores|underscoring|foster|fosters|fostering|vibrant|meticulous|meticulously|intricate|enhance|enhances|bolster|bolsters|garner|garners|elevate|elevates|empower|empowers|unlock|unlocks|navigate|navigating|journey|cutting-edge|game-changer|ever-evolving|multifaceted|holistic|synergy|actionable)\b"
PATTERNS = [
    ("DASH", r" -- | — |—"),
    ("VOCAB", VOCAB),
    ("COPULA", r"\b(serves as|stands as|functions as|plays a (vital|key|crucial|pivotal) role|boasts)\b"),
    ("NOTXBUTY", r"\b(not only\b.*\bbut also|not just\b.*\b(but|it'?s|it is)\b|isn'?t just|is not just|it'?s not [^.;]{1,40}, it'?s)"),
    ("HOOK", r"\b(here'?s the thing|here is the thing|the key insight|the real power|the hard truth|the irony|let that sink in|what this means is|the bottom line)\b"),
    ("RECAP", r"^\s*(in summary|in conclusion|overall|ultimately|in short|to summarise|to summarize)\b"),
    ("HEDGE", r"\b(it'?s worth noting|it is worth noting|importantly|notably|essentially|arguably|needless to say|of course)\b"),
    ("HONEST", r"\b(honest|honestly|candidly|frankly|to be clear|nothing is mocked|for real|the real thing|genuinely)\b"),
    ("TAIL", r",\s+(highlighting|underscoring|ensuring|reflecting|contributing to|showcasing|demonstrating|emphasi[sz]ing|allowing|enabling|making it|marking a|signalling|signaling)\b"),
    ("MANNERED", r"\b(closes? the door|the door (on|to)|nobody'?s word|at its word|on its word|earns? its keep|pays? for itself|worth (turning|keeping|watching|knowing|having)|load-bearing|under the hood|at the end of the day|on the table|moving parts|the bar to beat|ground story|footing|the way a real|which is why|written down|for the record|keeps? (its|your|their) hours|hands? (it |them )?(back|over)|comes? into its own)\b"),
    ("INFLATE", r"\b(groundbreaking|revolutionary|transformative|unprecedented|world-class|state-of-the-art|best-in-class|widely regarded|experts (say|agree|argue)|a wide range of|rich history)\b"),
    ("QUESTION", r"\?\s*$"),
]
RES = [(c, re.compile(p, re.I)) for c, p in PATTERNS]

def md_lines(path):
    """Yield (lineno, text) for prose lines in a Markdown file."""
    fence = False
    for i, line in enumerate(open(path, encoding="utf-8"), 1):
        s = line.rstrip("\n")
        if s.strip().startswith("```"):
            fence = not fence
            continue
        if fence or s.startswith("    ") or s.startswith("\t") or s.lstrip().startswith("|"):
            continue
        yield i, s

def html_lines(path):
    """Yield (lineno, text) for visible text lines in an HTML file."""
    src = open(path, encoding="utf-8").read()
    src = re.sub(r"<(script|style|pre|code)\b.*?</\1>", lambda m: "\n" * m.group(0).count("\n"), src, flags=re.S | re.I)
    for i, line in enumerate(src.split("\n"), 1):
        text = html.unescape(re.sub(r"<[^>]+>", " ", line))
        alts = " ".join(re.findall(r'(?:alt|title|content)="([^"]*)"', line))
        text = (text + " " + alts).strip()
        if text:
            yield i, text

def scan():
    hits = 0
    files = sorted(glob.glob(os.path.join(ROOT, "**/*.md"), recursive=True)) + sorted(glob.glob(os.path.join(ROOT, "docs/**/*.html"), recursive=True))
    for path in files:
        if any(k in path for k in SKIP):
            continue
        lines = html_lines(path) if path.endswith(".html") else md_lines(path)
        for i, text in lines:
            for code, rx in RES:
                m = rx.search(text)
                if m:
                    hits += 1
                    excerpt = text.strip()
                    j = max(0, m.start() - 40)
                    print(f"{os.path.relpath(path, ROOT)}:{i}: {code}: …{excerpt[j:j+110]}…")
    print(f"{hits} hit(s)")
    return hits

if __name__ == "__main__":
    sys.exit(1 if scan() else 0)
