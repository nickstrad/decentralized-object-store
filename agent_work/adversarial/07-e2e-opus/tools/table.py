#!/usr/bin/env python3
"""Render evidence/run-all.txt as a markdown table (result, claim, check, detail)."""
import sys
print("| Result | Claim | Check | Detail |")
print("|---|---|---|---|")
for line in open(sys.argv[1]):
    line = line.rstrip("\n")
    if not line or line.startswith("#") or line.startswith("SUMMARY"):
        continue
    word, claim, rest = line.split(" ", 2)
    check, _, detail = rest.partition(": ")
    detail = detail.replace("|", "\\|")
    if len(detail) > 420:
        detail = detail[:417] + "..."
    print(f"| {word} | {claim} | `{check}` | {detail} |")
