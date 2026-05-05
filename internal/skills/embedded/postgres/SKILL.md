---
name: postgres
description: Execute safe read-only SQL against configured PostgreSQL databases (SELECT, EXPLAIN, SHOW, schema introspection). Use when exploring schemas, validating data, or debugging app behaviour against Postgres — not for migrations or writes.
version: 1.0.1
---

# PostgreSQL (read-only)

**Portability:** Works with any agent that can run **Python 3** and reach the database (VPN/network as required). **Script:** `scripts/query.py`  
**Upstream basis:** [sanjay3290/ai-skills — postgres](https://github.com/sanjay3290/ai-skills/tree/main/skills/postgres) (Apache-2.0).

## Setup

1. Install dependencies:

```bash
pip install -r skills/postgres/requirements.txt
```

2. Create `connections.json` in the skill directory next to `SKILL.md`, **or** in a shared config path your environment documents, for example:
   - `~/.config/claude/postgres-connections.json`
   - `~/.config/opencode/postgres-connections.json`
   - or the same filename under your assistant’s config root if different

Use mode `600` on Unix:

```bash
chmod 600 skills/postgres/connections.json
```

```json
{
  "databases": [
    {
      "name": "production",
      "description": "Main app — users, orders",
      "host": "db.example.com",
      "port": 5432,
      "database": "app_prod",
      "user": "readonly_user",
      "password": "your-password",
      "sslmode": "require"
    }
  ]
}
```

| Field | Required | Notes |
|-------|----------|--------|
| name | Yes | Short id for `--db` |
| description | Yes | Helps pick the right DB |
| host, database, user, password | Yes | |
| port | No | Default 5432 |
| sslmode | No | `disable`, `allow`, `prefer`, `require`, … |

## Usage

From the **workspace root** (adjust the path if the skill is installed elsewhere):

```bash
python3 skills/postgres/scripts/query.py --list
python3 skills/postgres/scripts/query.py --db production --tables
python3 skills/postgres/scripts/query.py --db production --schema
python3 skills/postgres/scripts/query.py --db production --query "SELECT id, email FROM users LIMIT 20"
```

## Safety

- Connection uses PostgreSQL **read-only** session mode.
- Client-side checks allow only read-style statements; single statement per invocation.
- Statement timeout and row caps are enforced in the script.

## Picking a database

Match user intent to `description` (e.g. “orders” → DB whose description mentions orders). If unclear, `--list` and ask.
