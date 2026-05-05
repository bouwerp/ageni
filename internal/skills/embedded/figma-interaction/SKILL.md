---
name: figma-interaction
description: Figma integration via MCP server or REST API. Use when the user asks to extract design tokens, generate code from Figma designs, read file structure, or fetch node details. Works with any MCP-capable AI assistant; falls back to REST + a personal access token when MCP is unavailable.
version: 3.0.0
platforms:
  - opencode
  - claude-code
  - cursor
  - gemini
  - any-mcp-client
---

# Figma Interaction - Universal Skill

This skill provides **platform-agnostic** guidance for interacting with Figma designs. It works with any MCP-capable AI assistant or falls back to REST API.

## Quick Start

**If your AI supports MCP:**
```
Connect to Figma MCP server: https://mcp.figma.com/mcp
Authenticate via OAuth when prompted
Use tools: get_design_context, get_variable_defs, get_screenshot, etc.
```

**If your AI doesn't support MCP:**
```bash
export FIGMA_PERSONAL_TOKEN='figd_...'
Use REST API tools: get_file, get_node, get_variables, etc.
```

---

## MCP Server Configuration (Universal)

**Server Details:**
- **URL:** `https://mcp.figma.com/mcp`
- **Transport:** HTTP (Streamable)
- **Authentication:** OAuth 2.0 (authorization code + PKCE)
- **Scope:** `mcp:connect`

**Generic MCP Config (JSON):**
```json
{
  "mcpServers": {
    "figma": {
      "url": "https://mcp.figma.com/mcp",
      "transport": "http",
      "authentication": "oauth"
    }
  }
}
```

**Platform-Specific Setup:**

<details>
<summary>OpenCode</summary>

```bash
# Add to ~/.config/opencode/opencode.json
{
  "mcp": {
    "figma": {
      "type": "remote",
      "url": "https://mcp.figma.com/mcp",
      "oauth": {}
    }
  }
}

# Authenticate
opencode mcp auth figma
```

</details>

<details>
<summary>Claude Code</summary>

```bash
# Recommended: Official plugin
claude plugin install figma@claude-plugins-official

# Or manual MCP
claude mcp add --transport http figma https://mcp.figma.com/mcp
claude mcp auth figma
```

</details>

<details>
<summary>Cursor</summary>

In chat: `/plugin-add figma`

Or: Settings → MCP → Add server → `https://mcp.figma.com/mcp`

</details>

<details>
<summary>Other MCP Clients</summary>

Configure your MCP client to connect to `https://mcp.figma.com/mcp` with OAuth authentication. Refer to your client's documentation for MCP server configuration.

</details>

---

## Available MCP Tools

These tools are available when connected to the Figma MCP server:

### `get_design_context`
**Purpose:** Generate code from Figma frames (React + Tailwind by default)

**Input:** Figma URL or node ID
**Output:** Structured code representation

**Example Prompt:**
```
Get design context for this Figma frame: https://www.figma.com/file/ABC123?node-id=456-789
Generate React code with Tailwind CSS
```

**Customization:**
- "Generate in Vue/React Native/SwiftUI instead"
- "Use components from src/components/ui"
- "Style with Chakra UI instead of Tailwind"

---

### `get_variable_defs`
**Purpose:** Extract design tokens (colors, spacing, typography)

**Input:** Figma URL or file key
**Output:** Design token definitions

**Example Prompt:**
```
Get all design tokens from this Figma file: <url>
Include colors, spacing, and typography variables
```

---

### `get_screenshot`
**Purpose:** Capture visual reference of Figma selection

**Input:** Figma URL or node ID
**Output:** Screenshot image URL

**Example Prompt:**
```
Get screenshot of this frame for visual reference: <url>
```

---

### `get_metadata`
**Purpose:** Get lightweight XML structure of design (for large files)

**Input:** Figma URL or file key
**Output:** XML metadata

**Example Prompt:**
```
Get metadata for this large design to see the structure: <url>
```

---

### `get_code_connect_map`
**Purpose:** Get mappings between Figma components and code components

**Input:** Figma file key
**Output:** Component mapping object

**Example Prompt:**
```
Get Code Connect mappings for this file: <url>
Show which Figma components link to our codebase
```

---

### `create_design_system_rules`
**Purpose:** Generate agent rules for design system consistency

**Input:** None (generates generic rules)
**Output:** Markdown rules file

**Example Prompt:**
```
Create design system rules for Figma implementation
Save to .opencode/rules/figma-design-system.md
```

---

### `generate_diagram`
**Purpose:** Create FigJam diagrams from Mermaid syntax

**Input:** Mermaid code or description
**Output:** FigJam diagram

**Example Prompt:**
```
Generate a flowchart for user authentication flow
Create sequence diagram for payment processing
```

---

## REST API Fallback (Universal)

If MCP is not available, use REST API with Personal Access Token.

**Setup:**
```bash
# Generate token: https://www.figma.com/developers/api#access-tokens
export FIGMA_PERSONAL_TOKEN='figd_your-token'
```

**Available REST API Tools:**

| Tool | Purpose | MCP Equivalent |
|------|---------|----------------|
| `get_file` | Get file structure | - |
| `get_node` | Get specific node | Partial `get_design_context` |
| `get_image` | Get rendered image | `get_screenshot` |
| `get_variables` | Get design tokens | `get_variable_defs` |
| `get_comments` | Get file comments | - |

**Example REST API Usage:**
```
Get the file structure from Figma file abc123xyz
Get node 456-789 from that file
Get design variables from the file
Get a screenshot of node 456-789 at 2x scale
```

---

## Universal Workflows

### Workflow 1: Generate Code from Design

**Steps:**
1. Copy Figma frame URL
2. Request design context
3. Review generated code
4. Iterate with refinements

**Universal Prompt:**
```
Implement this design from Figma: <url>

Requirements:
- Framework: React/Vue/iOS/etc.
- Styling: Tailwind/Chakra/CSS/etc.
- Use existing components from: <path>
- Follow accessibility guidelines (WCAG 2.1 AA)
```

**Expected Flow:**
1. AI connects to Figma MCP server
2. Calls `get_design_context` with node ID from URL
3. Calls `get_screenshot` for visual reference
4. Generates code based on design context
5. Presents code for review

---

### Workflow 2: Extract Design Tokens

**Steps:**
1. Provide Figma file URL
2. Request variable definitions
3. Structure into token format
4. Generate token files

**Universal Prompt:**
```
Extract all design tokens from this Figma file: <url>

Create:
1. JSON token file (tokens.json)
2. CSS custom properties (tokens.css)
3. TypeScript types (tokens.ts)

Include: colors, spacing, typography, effects
```

---

### Workflow 3: Create Design System Documentation

**Steps:**
1. Generate design system rules
2. Document components
3. Create usage guidelines
4. Save to documentation file

**Universal Prompt:**
```
Create comprehensive design system documentation from this Figma file: <url>

Include:
- Component catalog with usage examples
- Design token reference
- Accessibility guidelines
- Code snippets for each component
- Save to: docs/design-system.md
```

---

### Workflow 4: Large Design Implementation

**Steps:**
1. Get metadata first (lightweight)
2. Identify sections to implement
3. Implement section by section
4. Assemble into complete page

**Universal Prompt:**
```
This is a large design. Follow this approach:

1. Get metadata first to see the structure: <url>
2. List all top-level sections
3. Implement each section separately:
   - Header section
   - Main content area
   - Sidebar
   - Footer
4. Get screenshots for each section
5. Combine into complete page
```

---

## Authentication (Universal Patterns)

### OAuth Flow (MCP)

**Standard OAuth 2.0 with PKCE:**

1. **Authorization Request:**
   ```
   GET https://www.figma.com/oauth/mcp
     ?client_id={client_id}
     &redirect_uri={redirect_uri}
     &response_type=code
     &scope=mcp:connect
     &state={random_state}
     &code_challenge={pkce_challenge}
     &code_challenge_method=S256
   ```

2. **User Authorization:**
   - User opens URL in browser
   - Authorizes application
   - Redirected with authorization code

3. **Token Exchange:**
   ```
   POST https://api.figma.com/v1/oauth/token
   Content-Type: application/x-www-form-urlencoded

   grant_type=authorization_code
   code={authorization_code}
   redirect_uri={redirect_uri}
   code_verifier={pkce_verifier}
   ```

4. **Token Response:**
   ```json
   {
     "access_token": "...",
     "refresh_token": "...",
     "expires_in": 3600,
     "scope": "mcp:connect",
     "token_type": "Bearer"
   }
   ```

### Headless OAuth (Manual Flow)

For environments without browser access:

```bash
# 1. Generate authorization URL
figma_oauth_url

# 2. Copy URL to browser (on any device)
# 3. Authorize and copy code from redirect URL
# 4. Exchange for tokens
figma_oauth_token --code 'your-code' --codeVerifier 'your-verifier'

# 5. Save tokens for future use
export FIGMA_ACCESS_TOKEN='...'
export FIGMA_REFRESH_TOKEN='...'
```

### Personal Access Token (REST API)

**Generate:**
1. Visit: https://www.figma.com/developers/api#access-tokens
2. Click "Get personal access token"
3. Copy token (starts with `figd_`)

**Use:**
```bash
export FIGMA_PERSONAL_TOKEN='figd_...'

# All REST API calls use this header:
# X-Figma-Token: $FIGMA_PERSONAL_TOKEN
```

---

## Error Handling

### Common Errors

**401 Unauthorized:**
- Token expired or invalid
- Solution: Re-authenticate or refresh token

**403 Forbidden:**
- Insufficient permissions on Figma file
- Solution: Request access from file owner

**404 Not Found:**
- Invalid file key or node ID
- Solution: Verify URL is correct

**429 Rate Limited:**
- Too many requests
- Solution: Wait and retry (check rate limit headers)

**MCP Connection Failed:**
- Server unreachable or OAuth expired
- Solution: Reconnect MCP server or re-authenticate

### Rate Limits

| Plan | Limit | Notes |
|------|-------|-------|
| Starter/View | 6/month | Very limited |
| Professional | 120/minute | Tier 1 REST API |
| Organization | 120/minute | Tier 1 REST API |
| Enterprise | 600/minute | Tier 2 (on request) |

**Best Practices:**
- Cache results locally
- Use `get_metadata` for large designs (lighter)
- Batch requests when possible
- Implement exponential backoff

---

## Best Practices (Universal)

### Figma File Structure

**For Better Code Generation:**

1. **Use Components** for reusable elements
2. **Name layers semantically** (`CardContainer` not `Group 5`)
3. **Use Auto Layout** for responsive behavior
4. **Use Variables** for design tokens
5. **Break large screens** into components

### Prompt Engineering

**Effective Prompts:**
```
✅ "Generate React + Tailwind from this Figma frame: <url>. Use components from src/components/ui."
❌ "Make this design into code" (too vague)

✅ "Extract all design tokens: colors, spacing, typography from <url>"
❌ "Get variables" (unclear)

✅ "Implement just the header section: <url>. Use flexbox for layout."
❌ "Code this whole page" (too large)
```

### Code Quality

**Implementation Guidelines:**
1. Treat MCP output as starting point
2. Translate to project conventions
3. Validate against Figma screenshot
4. Avoid hardcoded values
5. Follow accessibility guidelines
6. Document components

---

## Tool Reference

### MCP Tools Summary

| Tool | Code Gen | Tokens | Visual | Use Case |
|------|----------|--------|--------|----------|
| `get_design_context` | ✅ | ✅ | ✅ | Primary code generation |
| `get_variable_defs` | ❌ | ✅ | ❌ | Extract design tokens |
| `get_screenshot` | ❌ | ❌ | ✅ | Visual reference |
| `get_metadata` | ❌ | ❌ | ❌ | Large file structure |
| `get_code_connect_map` | ❌ | ❌ | ❌ | Component mapping |
| `create_design_system_rules` | ❌ | ❌ | ❌ | Generate rules |
| `generate_diagram` | ❌ | ❌ | ✅ | FigJam diagrams |

### REST API Tools Summary

| Tool | Purpose | Auth |
|------|---------|------|
| `get_file` | File structure | PAT |
| `get_node` | Node details | PAT |
| `get_image` | Rendered image | PAT |
| `get_variables` | Design tokens | PAT |
| `get_comments` | File comments | PAT |
| `figma_oauth_url` | Generate OAuth URL | None |
| `figma_oauth_token` | Exchange for tokens | None |
| `figma_oauth_refresh` | Refresh tokens | None |
| `figma_whoami` | Verify auth | OAuth |

---

## References

- **Figma REST API:** https://www.figma.com/developers/api
- **Figma MCP Server:** https://github.com/figma/mcp-server-guide
- **Atlassian Document Format:** https://developer.atlassian.com/cloud/jira/platform/apis/document/structure/
- **Code Connect:** https://help.figma.com/hc/en-us/articles/23920389749655-Code-Connect
- **Rate Limits:** https://www.figma.com/developers/api#rate-limits

---

## Syncing to Other AI Tools

This skill is designed to be **platform-agnostic** and should work with any AI assistant that supports:
- MCP servers (preferred)
- Custom tools
- REST API integration

**To sync to other tools:**
```bash
# Copy skill to target tool's skills directory
cp -r ~/.config/opencode/skills/figma-interaction /path/to/target/skills/
```

**Skill is compatible with:**
- ✅ OpenCode
- ✅ Claude Code
- ✅ Cursor
- ✅ Gemini (REST API only)
- ✅ Any MCP-capable AI assistant
