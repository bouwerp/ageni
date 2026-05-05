# Bundled skills

The skills embedded in this directory are sourced from the
[realfi-co/agent-skills](https://github.com/realfi-co/agent-skills) repository
under the **MIT License**.

The `webapp-testing` skill additionally credits
[Prat011/awesome-llm-skills](https://github.com/Prat011/awesome-llm-skills)
as the upstream source — see `webapp-testing/LICENSE.txt` for that
attribution.

Users can override any bundled skill by placing a directory of the same name
in `~/.ageni/skills/` (global) or `./.ageni/skills/` (project). The on-disk
copy wins over the embedded version.

To reinstall bundled skills' upstream into the on-disk location:

    ageni skills install git@github.com:realfi-co/agent-skills.git
