---
name: confluence
description: This skill should be used when the user asks to "view Confluence page", "update Confluence", "create Confluence page", "search Confluence", "summarize documentation", or works with Confluence REST API and Atlassian Document Format (ADF). Provides guidance on Confluence REST API interactions using ADF format.
version: 1.0.0
---

# Confluence Interaction

This skill provides guidance on interacting with Confluence via the REST API using Atlassian Document Format (ADF).

## Overview

Use this skill when:
- Viewing or reading Confluence pages
- Creating new Confluence pages
- Updating existing Confluence pages
- Searching through Confluence documentation
- Summarizing Confluence content
- Working with Confluence REST API and ADF format
- Managing page hierarchies and spaces

## Authentication Setup

### Priority Order for Credentials

1. **Environment Variables** (highest priority - use if available)
   - `ATLASSIAN_API_TOKEN` (primary, works for both JIRA and Confluence)
   - `ATLASSIAN_EMAIL`
   - `ATLASSIAN_SITE` (e.g., `your-domain.atlassian.net`)
   - Legacy: `CONFLUENCE_API_TOKEN`, `CONFLUENCE_EMAIL`, `CONFLUENCE_SITE`

2. **ACLI Config File** (medium priority - decode if env vars not set)
   - Located at `~/.config/acli/confluence_config.yaml` or `~/.config/acli/jira_config.yaml`
   - Decode OAuth token from config

3. **Ask User Directly** (fallback - if neither env nor config available)
   - Request API token from user
   - Token URL: https://id.atlassian.com/manage-profile/security/api-tokens

### Checking Environment Variables

```bash
# Check if env vars are available (ATLASSIAN_* is primary, CONFLUENCE_* for backward compat)
if [ -n "$ATLASSIAN_API_TOKEN" ]; then
  API_TOKEN="$ATLASSIAN_API_TOKEN"
  EMAIL="${ATLASSIAN_EMAIL:-$CONFLUENCE_EMAIL}"
  SITE="${ATLASSIAN_SITE:-$CONFLUENCE_SITE:-$CONFLUENCE_DOMAIN}"
elif [ -n "$CONFLUENCE_API_TOKEN" ]; then
  API_TOKEN="$CONFLUENCE_API_TOKEN"
  EMAIL="${CONFLUENCE_EMAIL:-$ATLASSIAN_EMAIL}"
  SITE="${CONFLUENCE_SITE:-$ATLASSIAN_SITE:-$CONFLUENCE_DOMAIN}"
fi
```

### Decoding from ACLI Config

```bash
# Parse ACLI config for Confluence
ACLI_CONFIG="$HOME/.config/acli/confluence_config.yaml"

if [ -f "$ACLI_CONFIG" ]; then
  # Extract email from config
  EMAIL=$(grep 'email:' "$ACLI_CONFIG" | head -1 | sed 's/.*email: //')
  
  # For OAuth tokens, you may need to extract from ACLI's token storage
  # ACLI uses OAuth 2.0, tokens stored in ~/.config/acli/oauth_tokens.yaml
  OAUTH_CONFIG="$HOME/.config/acli/oauth_tokens.yaml"
  if [ -f "$OAUTH_CONFIG" ]; then
    # Extract access token (base64 encoded in some cases)
    ACCESS_TOKEN=$(grep 'access_token:' "$OAUTH_CONFIG" | head -1 | sed 's/.*access_token: //')
  fi
fi
```

### Constructing API Headers

```bash
# Using API token
JIRA_SITE="realfi.atlassian.net"
CONFLUENCE_SITE="$JIRA_SITE"  # Same domain for Cloud
EMAIL="pieter.bouwer@iohk.io"
API_TOKEN="user-provided-token-or-from-env"

# Test the connection
curl -u "$EMAIL:$API_TOKEN" \
  "https://$CONFLUENCE_SITE/wiki/rest/api/user/current" \
  -H "Accept: application/json"
```

## Atlassian Document Format (ADF) for Confluence

Confluence uses ADF v1, same as JIRA, with additional Confluence-specific macros.

### Basic ADF Structure

```json
{
  "version": 1,
  "type": "doc",
  "content": [
    {
      "type": "paragraph",
      "content": [
        {
          "type": "text",
          "text": "Your text here"
        }
      ]
    }
  ]
}
```

### Common ADF Elements

#### Paragraph with Formatting

```json
{
  "type": "paragraph",
  "content": [
    {
      "type": "text",
      "text": "Bold text",
      "marks": [{"type": "strong"}]
    },
    {
      "type": "text",
      "text": " and "
    },
    {
      "type": "text",
      "text": "italic text",
      "marks": [{"type": "em"}]
    },
    {
      "type": "text",
      "text": " and ",
      "marks": [{"type": "code"}]
    }
  ]
}
```

#### Headings

```json
{
  "type": "heading",
  "attrs": {"level": 1},
  "content": [{"type": "text", "text": "Main Title"}]
}
```

Levels: 1-6 (H1-H6)

#### Bullet List

```json
{
  "type": "bulletList",
  "content": [
    {
      "type": "listItem",
      "content": [
        {
          "type": "paragraph",
          "content": [{"type": "text", "text": "Item 1"}]
        }
      ]
    },
    {
      "type": "listItem",
      "content": [
        {
          "type": "paragraph",
          "content": [{"type": "text", "text": "Item 2"}]
        }
      ]
    }
  ]
}
```

#### Ordered List

```json
{
  "type": "orderedList",
  "attrs": {"order": 1},
  "content": [
    {
      "type": "listItem",
      "content": [
        {
          "type": "paragraph",
          "content": [{"type": "text", "text": "First step"}]
        }
      ]
    }
  ]
}
```

#### Code Block

```json
{
  "type": "codeBlock",
  "attrs": {"language": "python"},
  "content": [
    {
      "type": "text",
      "text": "def hello():\n    print('Hello World')"
    }
  ]
}
```

#### Link

```json
{
  "type": "text",
  "text": "Click here",
  "marks": [
    {
      "type": "link",
      "attrs": {"href": "https://example.com"}
    }
  ]
}
```

#### Confluence Macro: Info Panel

```json
{
  "type": "confluenceContentMacro",
  "attrs": {"macroId": "info", "macroName": "info", "macroSchemaVersion": 1},
  "content": [
    {
      "type": "paragraph",
      "content": [{"type": "text", "text": "This is an info panel"}]
    }
  ]
}
```

#### Confluence Macro: Warning Panel

```json
{
  "type": "confluenceContentMacro",
  "attrs": {"macroId": "warning", "macroName": "warning", "macroSchemaVersion": 1},
  "content": [
    {
      "type": "paragraph",
      "content": [{"type": "text", "text": "This is a warning"}]
    }
  ]
}
```

#### Confluence Macro: Code Block (Rich)

```json
{
  "type": "confluenceContentMacro",
  "attrs": {"macroId": "code", "macroName": "code", "macroSchemaVersion": 1},
  "content": [
    {
      "type": "confluenceContentMacroParameter",
      "attrs": {"parameterKey": "language", "parameterValue": "javascript"}
    },
    {
      "type": "confluenceContentMacroBody",
      "content": [{"type": "text", "text": "console.log('Hello')"}]
    }
  ]
}
```

## API Operations

### 1. Get Current User (Test Connection)

```bash
CONFLUENCE_SITE="realfi.atlassian.net"
EMAIL="your.email@example.com"
API_TOKEN="your-api-token"

curl -u "$EMAIL:$API_TOKEN" \
  "https://$CONFLUENCE_SITE/wiki/rest/api/user/current" \
  -H "Accept: application/json" | jq .
```

### 2. Search Confluence Pages

```bash
# Search by keyword (CQL - Confluence Query Language)
CQL_QUERY="type = page AND space = \"PROJ\" AND text ~ \"API documentation\""

curl -u "$EMAIL:$API_TOKEN" \
  "https://$CONFLUENCE_SITE/wiki/rest/api/content/search?cql=$CQL_QUERY&expand=body.storage,version" \
  -H "Accept: application/json" | jq .
```

**Common CQL Queries:**
- `type = page AND space = "SPACE_KEY"` - All pages in a space
- `type = page AND title = "Page Title"` - Specific page by title
- `type = page AND creator = currentUser()` - Pages you created
- `type = page AND lastModified > "2024-01-01"` - Recently modified
- `type = page AND label = "documentation"` - Pages with label

### 3. Get Page by ID

```bash
PAGE_ID="123456789"

curl -u "$EMAIL:$API_TOKEN" \
  "https://$CONFLUENCE_SITE/wiki/rest/api/content/$PAGE_ID?expand=body.storage,version,ancestors" \
  -H "Accept: application/json" | jq .
```

**Key fields:**
- `title` - Page title
- `body.storage.value` - Page content in storage format (HTML-like)
- `version.number` - Version number
- `ancestors` - Parent page hierarchy
- `space.key` - Space key
- `space.name` - Space name

### 4. Get Page by Title

```bash
SPACE_KEY="PROJ"
TITLE="API Documentation"

curl -u "$EMAIL:$API_TOKEN" \
  "https://$CONFLUENCE_SITE/wiki/rest/api/content?spaceKey=$SPACE_KEY&title=$TITLE&expand=body.storage,version" \
  -H "Accept: application/json" | jq '.results[0]'
```

### 5. Create New Page

```bash
SPACE_KEY="PROJ"
PARENT_ID="123456789"  # Optional: parent page ID
TITLE="New Documentation Page"

# Create ADF content
ADF_CONTENT='{
  "version": 1,
  "type": "doc",
  "content": [
    {
      "type": "heading",
      "attrs": {"level": 1},
      "content": [{"type": "text", "text": "New Documentation Page"}]
    },
    {
      "type": "paragraph",
      "content": [{"type": "text", "text": "This is the page content."}]
    }
  ]
}'

curl -u "$EMAIL:$API_TOKEN" \
  -X POST \
  "https://$CONFLUENCE_SITE/wiki/rest/api/content" \
  -H "Content-Type: application/json" \
  -d "{
    \"type\": \"page\",
    \"title\": \"$TITLE\",
    \"space\": {\"key\": \"$SPACE_KEY\"},
    \"body\": {
      \"storage\": {
        \"value\": $ADF_CONTENT,
        \"representation\": \"storage\"
      }
    }
    $(if [ -n "$PARENT_ID" ]; then echo ",\"ancestors\": [{\"id\": \"$PARENT_ID\"}]"; fi)
  }" | jq .
```

### 6. Update Existing Page

```bash
PAGE_ID="123456789"

# First, get current version number
CURRENT_VERSION=$(curl -u "$EMAIL:$API_TOKEN" \
  "https://$CONFLUENCE_SITE/wiki/rest/api/content/$PAGE_ID?version=number" \
  -H "Accept: application/json" | jq '.version.number')

NEW_VERSION=$((CURRENT_VERSION + 1))

# Create new ADF content
ADF_CONTENT='{
  "version": 1,
  "type": "doc",
  "content": [
    {
      "type": "heading",
      "attrs": {"level": 1},
      "content": [{"type": "text", "text": "Updated Title"}]
    },
    {
      "type": "paragraph",
      "content": [{"type": "text", "text": "Updated content here."}]
    }
  ]
}'

curl -u "$EMAIL:$API_TOKEN" \
  -X PUT \
  "https://$CONFLUENCE_SITE/wiki/rest/api/content/$PAGE_ID" \
  -H "Content-Type: application/json" \
  -d "{
    \"id\": \"$PAGE_ID\",
    \"type\": \"page\",
    \"title\": \"Updated Title\",
    \"version\": {
      \"number\": $NEW_VERSION
    },
    \"body\": {
      \"storage\": {
        \"value\": $ADF_CONTENT,
        \"representation\": \"storage\"
      }
    }
  }" | jq .
```

### 7. Get Space Information

```bash
SPACE_KEY="PROJ"

curl -u "$EMAIL:$API_TOKEN" \
  "https://$CONFLUENCE_SITE/wiki/rest/api/space/$SPACE_KEY?expand=homepage,description" \
  -H "Accept: application/json" | jq .
```

### 8. List All Spaces

```bash
curl -u "$EMAIL:$API_TOKEN" \
  "https://$CONFLUENCE_SITE/wiki/rest/api/space?expand=description,homepage" \
  -H "Accept: application/json" | jq .
```

### 9. Add Comment to Page

```bash
PAGE_ID="123456789"

curl -u "$EMAIL:$API_TOKEN" \
  -X POST \
  "https://$CONFLUENCE_SITE/wiki/rest/api/content/$PAGE_ID/child/comment" \
  -H "Content-Type: application/json" \
  -d '{
    "body": {
      "storage": {
        "value": {
          "version": 1,
          "type": "doc",
          "content": [
            {
              "type": "paragraph",
              "content": [{"type": "text", "text": "This is a comment"}]
            }
          ]
        },
        "representation": "storage"
      }
    }
  }' | jq .
```

### 10. Get Page Comments

```bash
PAGE_ID="123456789"

curl -u "$EMAIL:$API_TOKEN" \
  "https://$CONFLUENCE_SITE/wiki/rest/api/content/$PAGE_ID/child/comment?expand=body.storage" \
  -H "Accept: application/json" | jq .
```

### 11. Search and Summarize Documentation

```bash
# Search for pages matching a topic
CQL_QUERY="type = page AND text ~ \"API authentication\""

# Get search results
RESULTS=$(curl -u "$EMAIL:$API_TOKEN" \
  "https://$CONFLUENCE_SITE/wiki/rest/api/content/search?cql=$CQL_QUERY&expand=body.storage&limit=10" \
  -H "Accept: application/json")

# Extract and summarize
echo "$RESULTS" | jq '
  .results | map({
    title: .title,
    space: .space.name,
    excerpt: (.body.storage.value | tostring | .[0:200] + "..."),
    url: ._links.webui
  })
'
```

## Complete Example: Search and Update Documentation

```bash
#!/bin/bash

# Configuration - Try env vars first, then ACLI config
if [ -n "$CONFLUENCE_API_TOKEN" ]; then
  API_TOKEN="$CONFLUENCE_API_TOKEN"
  EMAIL="${CONFLUENCE_EMAIL:-$CONFLUENCE_EMAIL}"
  SITE="${CONFLUENCE_SITE:-$CONFLUENCE_DOMAIN}"
else
  # Fallback to ACLI config
  ACLI_CONFIG="$HOME/.config/acli/confluence_config.yaml"
  if [ -f "$ACLI_CONFIG" ]; then
    EMAIL=$(grep 'email:' "$ACLI_CONFIG" | head -1 | sed 's/.*email: //')
    # Note: For OAuth, you'd need to extract from oauth_tokens.yaml
    # For now, ask user
    echo "API token not found in environment. Please provide your Confluence API token:"
    echo "Generate one at: https://id.atlassian.com/manage-profile/security/api-tokens"
    read -s API_TOKEN
  fi
  SITE="realfi.atlassian.net"
fi

CONFLUENCE_SITE="$SITE"

# Search for pages about "API documentation"
CQL_QUERY="type = page AND text ~ \"API documentation\" AND space = \"PROJ\""

echo "Searching for pages..."
RESULTS=$(curl -s -u "$EMAIL:$API_TOKEN" \
  "https://$CONFLUENCE_SITE/wiki/rest/api/content/search?cql=$CQL_QUERY&expand=body.storage,version" \
  -H "Accept: application/json")

# Count results
COUNT=$(echo "$RESULTS" | jq '.results | length')
echo "Found $COUNT pages"

# Show results
echo "$RESULTS" | jq -r '.results[] | "\(.title) (v\(.version.number)) - \(.space.name)"'

# Update first result
if [ "$COUNT" -gt 0 ]; then
  PAGE_ID=$(echo "$RESULTS" | jq -r '.results[0].id')
  CURRENT_VERSION=$(echo "$RESULTS" | jq -r '.results[0].version.number')
  NEW_VERSION=$((CURRENT_VERSION + 1))
  
  echo "Updating page $PAGE_ID to version $NEW_VERSION..."
  
  # Create updated ADF content
  ADF_CONTENT='{
    "version": 1,
    "type": "doc",
    "content": [
      {
        "type": "heading",
        "attrs": {"level": 1},
        "content": [{"type": "text", "text": "API Documentation (Updated)"}]
      },
      {
        "type": "paragraph",
        "content": [
          {"type": "text", "text": "This documentation was automatically updated."}
        ]
      }
    ]
  }'
  
  curl -s -u "$EMAIL:$API_TOKEN" \
    -X PUT \
    "https://$CONFLUENCE_SITE/wiki/rest/api/content/$PAGE_ID" \
    -H "Content-Type: application/json" \
    -d "{
      \"id\": \"$PAGE_ID\",
      \"type\": \"page\",
      \"title\": \"API Documentation (Updated)\",
      \"version\": {\"number\": $NEW_VERSION},
      \"body\": {
        \"storage\": {
          \"value\": $ADF_CONTENT,
          \"representation\": \"storage\"
        }
      }
    }" | jq .
  
  echo "Page updated successfully!"
fi
```

## Complete Example: Summarize Documentation Space

```bash
#!/bin/bash

# Summarize all pages in a space
SPACE_KEY="PROJ"
CONFLUENCE_SITE="realfi.atlassian.net"
EMAIL="your.email@example.com"
API_TOKEN="your-api-token"

echo "Fetching all pages in space $SPACE_KEY..."

# Get all pages in space
PAGES=$(curl -s -u "$EMAIL:$API_TOKEN" \
  "https://$CONFLUENCE_SITE/wiki/rest/api/content?spaceKey=$SPACE_KEY&type=page&expand=body.storage,ancestors&limit=50" \
  -H "Accept: application/json")

# Generate summary
echo "$PAGES" | jq '
  {
    space: .results[0].space.name,
    total_pages: (.results | length),
    pages: [.results[] | {
      title: .title,
      parent: (if .ancestors then .ancestors[-1].title else "Root" end),
      word_count: (.body.storage.value | tostring | split(" ") | length),
      url: ._links.webui
    }],
    summary: "Space contains \(.results | length) pages covering various topics"
  }
'
```

## Common Errors and Solutions

### Error: 401 Unauthorized

**Cause:** Invalid or expired API token

**Solution:**
1. Check environment variables: `echo $CONFLUENCE_API_TOKEN`
2. Check ACLI config: `cat ~/.config/acli/confluence_config.yaml`
3. **Ask user for fresh API token** if needed
4. Generate new token at: https://id.atlassian.com/manage-profile/security/api-tokens

### Error: 403 Forbidden

**Cause:** Insufficient permissions

**Solution:**
1. Verify user has view/edit permissions on the space
2. Check space permissions in Confluence admin
3. Confirm API token has required scopes

### Error: 404 Not Found

**Cause:** Page/space doesn't exist or wrong site

**Solution:**
1. Verify page ID or space key
2. Check Confluence site domain is correct
3. Confirm content hasn't been deleted

### Error: 409 Conflict - Version Mismatch

**Cause:** Page was modified by someone else (version number outdated)

**Solution:**
1. Fetch current page to get latest version number
2. Increment version number: `NEW_VERSION = CURRENT_VERSION + 1`
3. Retry update

### Error: 400 Bad Request - Invalid ADF

**Cause:** Malformed ADF JSON

**Solution:**
1. Validate JSON syntax with `jq .`
2. Ensure root element is `type: "doc"`
3. Check `version` is set to 1
4. Verify all nodes have required `type` field

## Best Practices

1. **Credential Priority**: Always check env vars first, then ACLI config, then ask user
2. **Version Management**: Always fetch current version before updating, increment by 1
3. **Validate ADF**: Use `jq .` to check JSON syntax before sending
4. **Test with GET first**: Verify connectivity and permissions with read operations
5. **Handle pagination**: Use `limit` and `start` parameters for large result sets
6. **Use CQL effectively**: Leverage Confluence Query Language for precise searches
7. **Respect hierarchy**: Consider parent-child relationships when creating pages
8. **Summarize efficiently**: Extract key information without fetching full content

## Credential Helper Functions

```bash
# Function to get Confluence credentials
get_confluence_credentials() {
  # Priority 1: Environment variables
  if [ -n "$CONFLUENCE_API_TOKEN" ]; then
    echo "$CONFLUENCE_EMAIL:$CONFLUENCE_API_TOKEN:$CONFLUENCE_SITE"
    return 0
  fi
  
  # Priority 2: ACLI config
  ACLI_CONFIG="$HOME/.config/acli/confluence_config.yaml"
  if [ -f "$ACLI_CONFIG" ]; then
    EMAIL=$(grep 'email:' "$ACLI_CONFIG" | head -1 | sed 's/.*email: //')
    SITE=$(grep 'site:' "$ACLI_CONFIG" | head -1 | sed 's/.*site: //')
    
    # Try to get token from OAuth config
    OAUTH_CONFIG="$HOME/.config/acli/oauth_tokens.yaml"
    if [ -f "$OAUTH_CONFIG" ]; then
      TOKEN=$(grep 'access_token:' "$OAUTH_CONFIG" | head -1 | sed 's/.*access_token: //')
      if [ -n "$TOKEN" ]; then
        echo "$EMAIL:$TOKEN:$SITE"
        return 0
      fi
    fi
  fi
  
  # Priority 3: Ask user
  echo "Confluence credentials not found in environment or config."
  echo "Please provide your Confluence API token:"
  echo "Generate one at: https://id.atlassian.com/manage-profile/security/api-tokens"
  read -s API_TOKEN
  echo ""
  read -p "Email: " EMAIL
  read -p "Site (e.g., your-domain.atlassian.net): " SITE
  
  echo "$EMAIL:$API_TOKEN:$SITE"
}

# Usage:
# CREDENTIALS=$(get_confluence_credentials)
# EMAIL=$(echo "$CREDENTIALS" | cut -d: -f1)
# API_TOKEN=$(echo "$CREDENTIALS" | cut -d: -f2)
# SITE=$(echo "$CREDENTIALS" | cut -d: -f3)
```

## CQL (Confluence Query Language) Reference

```bash
# Search by text
text ~ "API documentation"
text ~ "authentication" AND text ~ "OAuth"

# Filter by space
space = "PROJ"
space in ("PROJ", "DEV", "DOCS")

# Filter by type
type = page
type = blogpost

# Filter by creator/modifier
creator = currentUser()
lastModifier = "username"

# Filter by date
created > "2024-01-01"
lastModified < "2024-06-01"

# Filter by label
label = "documentation"
label in ("api", "reference")

# Filter by parent/ancestor
parent = 123456789
ancestor = 123456789

# Filter by content status
contributor = currentUser()
favourite = true

# Combine with AND/OR/NOT
type = page AND space = "PROJ" AND text ~ "API"
type = page OR type = blogpost
NOT label = "draft"
```

## Syncing to Other AI Tools

This skill should be synced to other AI assistant tools (Claude Code, Gemini, Codex, etc.) using the skill-manager skill.

### Syncing Command

```bash
# Using skill-manager to sync to all tools
./scripts/sync-skills.sh --source ~/.config/opencode/skills/confluence-interaction
```

## References

- [Confluence REST API Documentation](https://developer.atlassian.com/cloud/confluence/rest/v3/intro/)
- [Atlassian Document Format](https://developer.atlassian.com/cloud/jira/platform/apis/document/structure/)
- [ADF Builder](https://developer.atlassian.com/cloud/jira/platform/apis/document/playground/)
- [Confluence CQL Reference](https://developer.atlassian.com/cloud/confluence/rest/v3/api-group-search/#cql-expressions)
- [Confluence Macros](https://developer.atlassian.com/cloud/confluence/apis/document/#macros)
