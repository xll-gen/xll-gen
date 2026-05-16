# Claude Code Instructions

**This file is intentionally minimal.** All durable agent guidance — architecture, co-change clusters, XLL/RTD/SHM lifecycle rules, improvement backlog — lives in [`AGENTS.md`](./AGENTS.md).

Before doing anything in this repository:

1. Read **[AGENTS.md](./AGENTS.md)** in full. It is the single source of truth.
2. If your change crosses the `shm`, `types`, or `sugar` repo boundary, read those repos' `AGENTS.md` too.
3. Do **not** add project-specific guidance to this file. Add it to `AGENTS.md` so every agent tool (Claude Code, Codex, Cursor, Aider, etc.) sees it.

Updating `CLAUDE.md` to anything other than this redirect is a policy violation; update `AGENTS.md` instead.
