---
name: ctf-workflow
description: Use for every CTF challenge to maintain evidence, artifacts, flag candidates, and a reproducible writeup.
---

# CTF workflow

1. Read the task scope, target allowlist, flag format, and supplied artifacts.
2. Inventory files before executing unknown binaries.
3. Form a short plan and test the least expensive hypotheses first.
4. Save reusable scripts and decisive outputs under `/workspace/artifacts`.
5. Treat matching flag text as a candidate until independently supported or verified.
6. Write `/workspace/result.json` and `/workspace/writeup.md` before finishing.

