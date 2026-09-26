# open-agents spawn

Spawn a worker or manager session in a registered project, or a standalone worker session that is not tied to a project.
Standalone sessions run in an Open Agents-managed directory. Register a project first with `open-agents project add` for project-scoped sessions.

## Syntax

```
open-agents spawn [flags]
```

## Flags

| Flag | Meaning | Default / Required |
|---|---|---|
| `--branch string` | Branch for the session worktree | `open-agents/<session-id>/root` |
| `--claim-pr string` | Immediately claim an existing PR for the spawned session | - |
| `--harness string` | Agent harness to use (see list below) | Project `worker.agent`; required if the project has none |
| `--issue string` | Issue id to associate with the session | - |
| `--kind string` | Session role: `worker` or `manager` | `worker` |
| `--name string` | Display name shown in the sidebar (max 20 characters) | Required |
| `--no-takeover` | Refuse if another active session owns the claimed PR (requires `--claim-pr`) | - |
| `--project string` | Project id to spawn the session in | Optional when `--standalone` is used; defaults to `OPEN_AGENTS_PROJECT_ID` or the current repo's registered project |
| `--standalone` | Spawn a projectless worker session in an Open Agents-managed directory | Disabled when `--project` is set |
| `--prompt string` | Initial prompt for the agent | - |

`--agent` is an alias for `--harness`.

Available harnesses: `opencode`, `aider`, `opencode`, `grok`, `droid`, `amp`, `agy`, `crush`, `cursor`, `qwen`, `copilot`, `goose`, `auggie`, `continue`, `devin`, `cline`, `kimi`, `kiro`, `kilocode`, `vibe`, `pi`, `autohand`.

## Examples

```bash
# Spawn a manager for a project (normally created through the manager API/UI)
open-agents spawn --project open-agents --kind manager --name "project-manager"
```

A manager starts in manager mode and may delegate. Workers created by a manager always start in planning mode; a planning-mode manager cannot delegate. A worker begins implementing only after the manager has reviewed its plan and run `open-agents build <worker-session-id>`.

```bash
# Spawn a worker for issue 142 in the open-agents project
open-agents spawn --project open-agents --issue 142 --name "fix-session-leak" --prompt "Fix the session leak described in issue 142. Branch off upstream/main."
```

```bash
# Spawn a worker and immediately claim an open PR
open-agents spawn --project open-agents --name "review-pr-88" --claim-pr 88 --harness opencode
```
