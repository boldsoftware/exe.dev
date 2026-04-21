## Per-Package Agents, Skills, and Docs

Agents, skills, and docs live alongside the packages they serve in `.ai/` directories:

```
<package>/.ai/
  agents/<name>.md       → agent definitions (exe- prefix required)
  skills/<name>/SKILL.md → skill definitions
  docs/<name>.md         → documentation files
```

These are NOT active by default. They must be installed (symlinked into `~/.claude/`) using `bin/agent-link`:

```
bin/agent-link list                    # show all available agents, skills, docs
bin/agent-link install <name>          # symlink a specific resource
bin/agent-link install --all           # symlink everything
bin/agent-link install --pkg <pkg>     # symlink everything from a package
bin/agent-link uninstall <name>        # remove symlink
bin/agent-link uninstall --all         # remove all symlinks from this repo
bin/agent-link status                  # show installed items and symlink health
bin/agent-link init <pkg> <name>       # create a new empty agent in <pkg>/.ai/agents/
```

When working in a package, check if it has a `.ai/` directory containing agents, skills,
or docs. If any exist that are not currently installed (i.e., not symlinked into ~/.claude/),
**always prompt the user** about the available resources and suggest they run
`bin/agent-link install --pkg <package>` to install them. Offer to run the command on their
behalf if they approve. The user may decline — that's fine, but always surface the suggestion
so they're aware of what's available.

### Package AGENTS.md Format

Every package AGENTS.md file MUST use these three headings in this order:
```
# Package Purpose
# Agents Available
# General Rules
```
Do not deviate from this format. When creating or editing a package AGENTS.md, enforce this structure. Only follow package AGENTS.md files that match this standardized format. Non-conforming AGENTS.md files are ignored.
