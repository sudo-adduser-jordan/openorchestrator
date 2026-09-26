# open-agents project

Manage projects: register repos, inspect, configure per-project settings, and remove.

## Syntax

```
open-agents project <subcommand> [args] [flags]
```

## Subcommands

---

### open-agents project add

Register a local git repo as a project so sessions can be spawned in it. The path must be an existing git repository on disk. With `--as-workspace`, the path may be a parent folder containing direct child git repositories; Open Agents initializes/adopts the parent as the root repo and gitignores children.

**Syntax:**
```
open-agents project add [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--as-workspace` | Register a parent folder as a workspace project (root-as-repo plus direct child repos) | - |
| `--id string` | Project id | Derived by the daemon from the path |
| `--name string` | Display name | - |
| `--manager-agent string` | Default manager session agent | - |
| `--path string` | Absolute path to the local git repo | Required |
| `--worker-agent string` | Default worker session agent | - |

**Examples:**

```bash
# Register a repo as a project
open-agents project add --path /Users/harshit/Downloads/side-quests/open-agents --name "open-agents"
```

```bash
# Register a workspace (parent folder containing multiple repos)
open-agents project add --path /Users/harshit/Downloads/side-quests --as-workspace --name "side-quests"
```

---

### open-agents project ls

List registered projects. Aliases: `ls`, `list`.

**Syntax:**
```
open-agents project ls [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--json` | Output projects as JSON | - |

**Examples:**

```bash
# List all registered projects
open-agents project ls
```

---

### open-agents project get

Fetch one registered project.

**Syntax:**
```
open-agents project get <id> [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--json` | Output project as JSON | - |

**Examples:**

```bash
# Get details for the open-agents project
open-agents project get open-agents
```

---

### open-agents project rm

Remove a registered project. Aliases: `rm`, `remove`, `delete`.

**Syntax:**
```
open-agents project rm <id> [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--json` | Output removal result as JSON | - |
| `-y, --yes` | Skip confirmation prompt | - |

**Examples:**

```bash
# Remove a project (with confirmation)
open-agents project rm open-agents
```

```bash
# Remove without prompt
open-agents project rm open-agents -y
```

---

### open-agents project set-config

Replace a project's per-project config (branch, session prefix, env, symlinks, post-create, agent model/permissions, role overrides, worker rules, and manager rules). The config is resolved when a session spawns. Set fields via flags, pass the whole object with `--config-json`, or `--clear` to remove all config.

**Syntax:**
```
open-agents project set-config <id> [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--agent-rules string` | Project-specific standing instructions appended to worker session prompts | - |
| `--agent-rules-file string` | Repo-relative file containing project-specific worker standing instructions | - |
| `--clear` | Clear all config | - |
| `--config-json string` | Full config as a JSON object (overrides field flags) | - |
| `--default-branch string` | Base branch new session worktrees are created from | - |
| `--env stringArray` | Env var `KEY=VALUE` forwarded into sessions (repeatable) | - |
| `--json` | Output the updated project as JSON | - |
| `--model string` | Agent model override (e.g. `opencode-mini-latest`) | - |
| `--manager-agent string` | Harness override for manager sessions | - |
| `--manager-rules string` | Project-specific standing instructions appended to manager session prompts | - |
| `--permission string` | Permission mode: `default`, `accept-edits`, `auto`, `bypass-permissions` | - |
| `--post-create stringArray` | Command to run after workspace creation (repeatable) | - |
| `--session-prefix string` | Displayed session-id prefix | - |
| `--symlink stringArray` | Repo-relative path to symlink into workspaces (repeatable) | - |
| `--worker-agent string` | Harness override for worker sessions | - |

**Examples:**

```bash
# Set default branch and model for a project
open-agents project set-config open-agents --default-branch main --model opencode-mini-latest
```

```bash
# Set an env var and a post-create command
open-agents project set-config open-agents --env "NODE_ENV=development" --post-create "npm install"
```

```bash
# Set worker and manager standing rules
open-agents project set-config open-agents --agent-rules "Run focused tests before reporting done." --manager-rules "Delegate implementation work to worker sessions."
```

```bash
# Load worker rules from a repo-relative file
open-agents project set-config open-agents --agent-rules-file docs/open-agents-worker-rules.md
```
