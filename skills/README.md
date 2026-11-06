# AI assistant skills

[`cppm/SKILL.md`](cppm/SKILL.md) teaches an AI coding assistant to drive the cppm CLI: the full
command and flag reference, the dependency spec forms, and the mistakes an assistant otherwise makes
— hand-writing `FetchContent` instead of running `cppm add`, or editing `CMakeLists.txt` to change
the C++ standard instead of editing `cppm.json` and running `cppm sync`.

## Install

For a single project, copy it into the project:

```sh
mkdir -p .claude/skills
cp -r /path/to/cppm/skills/cppm .claude/skills/
```

For every project, install it for your user:

```sh
mkdir -p ~/.claude/skills
cp -r /path/to/cppm/skills/cppm ~/.claude/skills/
```

Or without cloning:

```sh
mkdir -p ~/.claude/skills/cppm
curl -fsSL https://raw.githubusercontent.com/mehdi-shokohi/cppm/main/skills/cppm/SKILL.md \
  -o ~/.claude/skills/cppm/SKILL.md
```

The assistant loads it on its own when a task involves C++ projects, CMake or C++ dependencies.

## Other assistants

`SKILL.md` is plain Markdown with YAML frontmatter, so it works as context for any assistant.
For tools that read a single instructions file, append or point to its contents.

Note that the frontmatter `description` is what decides *when* an assistant reaches for the skill,
so keep its trigger wording if you adapt the file.

## Working on cppm itself

Different audience, different file: [`../CLAUDE.md`](../CLAUDE.md) covers contributing to this
repository — the build and test commands, and the constraints that must not be broken. `AGENTS.md`
is a symlink to it for assistants that look for that name.
