---
name: webapp-testing
description: Ad-hoc Python scripts that boot a local dev server and poke the UI for verification — capturing screenshots, reading console output, driving interactions via the Python API with a with_server helper. Triggers "screenshot the login page", "check the webapp in Chrome from Python", "verify the frontend against localhost", "poke the UI interactively".
version: 1.0.1
---

# Web application testing (local)

**Portability:** Requires **Python 3** and `playwright` installed for the interpreter you use (`pip install playwright` then `playwright install chromium`). Any agent that can run shell commands can use this workflow. **Paths:** examples use `skills/webapp-testing/scripts/` from the **agent-skills repo root**; if the skill lives under `~/.claude/skills/webapp-testing/` (or Cursor/OpenCode/Pi equivalents), substitute that base path.

Test **local** web applications with small **Python** Playwright scripts. Server lifecycle is handled by the bundled helper so agents do not need to manually manage background processes.

**Upstream:** Derived from [Prat011/awesome-llm-skills — webapp-testing](https://github.com/Prat011/awesome-llm-skills/tree/master/webapp-testing) (see `LICENSE.txt` in that repo for original license terms).

## Helper script

- `scripts/with_server.py` — starts one or more servers, waits for ports, runs your command, then tears down.

**Always run `--help` first** before reading the script source; invoke it as a black box.

From the repository root:

```bash
python3 skills/webapp-testing/scripts/with_server.py --help
```

## Decision tree

```
User task → Is it static HTML?
    ├─ Yes → Read HTML (or file://) to infer selectors → short Playwright script
    └─ No (dynamic) → Server running?
        ├─ No → with_server.py + minimal Playwright script
        └─ Yes → Reconnaissance: goto → networkidle → screenshot / DOM → then act
```

## Example: single dev server

```bash
python3 skills/webapp-testing/scripts/with_server.py \
  --server "npm run dev" --port 5173 -- \
  python3 your_automation.py
```

## Example: frontend + backend

```bash
python3 skills/webapp-testing/scripts/with_server.py \
  --server "cd backend && python server.py" --port 3000 \
  --server "cd frontend && npm run dev" --port 5173 -- \
  python3 your_automation.py
```

## Minimal automation template

```python
from playwright.sync_api import sync_playwright

with sync_playwright() as p:
    browser = p.chromium.launch(headless=True)
    page = browser.new_page()
    page.goto("http://localhost:5173")
    page.wait_for_load_state("networkidle")
    # ... assertions and actions ...
    browser.close()
```

## Reconnaissance-then-action

1. After `networkidle`, take a full-page screenshot or dump `page.content()`.
2. Derive selectors from what actually rendered (`get_by_role`, `text=`, test ids).
3. Execute clicks/fills; assert on visible outcomes.

## When to use Node Playwright instead

For persistent **test suites**, CI, fixtures, and multi-browser projects, prefer **`playwright-browser-automation`** (`@playwright/test`). Use **this** skill for quick local verification and agent-driven debugging loops.

## Common pitfall

Do not inspect the DOM on dynamic apps before waiting for load (prefer `wait_for_load_state("networkidle")` or robust `expect` locators).
