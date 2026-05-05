---
name: playwright-browser-automation
description: Node/TypeScript E2E test suites built on `@playwright/test` — fixtures, locators, network interception, trace debugging, video capture, CI pipelines. Triggers "@playwright/test", "playwright.config.ts", "npx playwright test", "write an E2E test in TypeScript", "add Playwright to CI".
version: 1.1.0
---
# Playwright Browser Automation

A structured approach to testing and automating web applications using Playwright.

**Companion skill:** `skills/webapp-testing/SKILL.md` focuses on **local** apps: starting dev servers, Python `sync_playwright` scripts, reconnaissance-then-action, and screenshots or console capture — while this skill centers on the **Node `@playwright/test`** workflow and project configuration.

**Portability:** Requires **Node.js** in the project under test; use `npx`/`npm` as documented by Playwright. Any agent that can edit files and run shell commands can follow this skill.

Provides safe patterns for browser automation, test creation, debugging, and validation of web application behavior.

## Core Principles

1. **Test before automating.** Write assertions to verify expected behavior before performing actions.
2. **Isolate tests.** Each test should run independently with clean browser state.
3. **Use locators wisely.** Prefer user-facing attributes and text over brittle selectors.
4. **Handle waiting automatically.** Leverage Playwright's auto-waiting mechanisms.
5. **Capture evidence.** Screenshots, videos, and traces help debug failures.
6. **Respect users.** Never perform harmful actions or access sensitive data without explicit permission.

## When to Use This Skill

- Testing frontend functionality of web applications
- Debugging UI behavior and user interactions
- Performing end-to-end (E2E) testing of user journeys
- Automating repetitive browser tasks for testing
- Validating responsive design across viewport sizes
- Testing authentication flows and authorization boundaries
- Verifying form submissions and input validation
- Checking API integration through UI
- Performance testing and monitoring

## When NOT to Use This Skill

- Testing pure backend logic without UI components
- Unit testing non-DOM JavaScript functions
- Load testing or stress testing (use specialized tools)
- Accessing production systems without explicit permission
- Automating actions that could cause harm or data loss

## Setup and Installation

### Prerequisites

- Node.js >= 14
- Compatible operating system (Windows, macOS, Linux)

### Installation Commands

```bash
# Initialize Playwright in your project
npm init playwright@latest

# Or install manually
npm install -D @playwright/test

# Install browsers
npx playwright install
```

### Configuration

Playwright creates a `playwright.config.ts` or `playwright.config.js` file. Key configuration options:

```typescript
// playwright.config.ts
import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: 'html',
  use: {
    baseURL: 'http://localhost:3000',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'firefox',
      use: { ...devices['Desktop Firefox'] },
    },
    {
      name: 'webkit',
      use: { ...devices['Desktop Safari'] },
    },
    {
      name: 'Mobile Chrome',
      use: { ...devices['Pixel 5'] },
    },
    {
      name: 'Mobile Safari',
      use: { ...devices['iPhone 12'] },
    },
  ],
});
```

## Test Structure and Organization

### Basic Test Format

```typescript
import { test, expect } from '@playwright/test';

test('should display homepage title', async ({ page }) => {
  await page.goto('https://example.com');
  await expect(page).toHaveTitle('Example Domain');
});
```

### Test Organization

```
tests/
├── homepage.spec.ts
├── auth/
│   ├── login.spec.ts
│   └── logout.spec.ts
├── e2e/
│   └── user-journey.spec.ts
└── components/
    ├── navigation.spec.ts
    └── forms.spec.ts
```

## Core Concepts

### Locators

Playwright encourages resilient locators that mimic how users find elements:

```typescript
// Preferred: User-facing attributes
await page.getByRole('button', { name: 'Submit' }).click();
await page.getByLabel('Email address').fill('user@example.com');
await page.getByPlaceholder('Enter your password').fill('secret');
await page.getByText('Welcome back').click();

// Semantic: ARIA roles and labels
await page.getByRole('heading', { name: 'Dashboard' });
await page.getByRole('link', { name: 'Profile' });

// Test IDs (when other options unavailable)
await page.getByTestId('submit-button').click();

// CSS/XPath (use sparingly)
await page.locator('#submit-btn').click();
await page.locator('button:has-text("Submit")').click();
```

### Actions

Common user actions with built-in waiting:

```typescript
// Navigation
await page.goto('https://example.com');
await page.goBack();
await page.goForward();
await page.reload();

// Clicking
await page.click('button');
// or
await page.getByRole('button').click();

// Typing
await page.fill('input[name="email"]', 'user@example.com');
// or
await page.getByLabel('Email').fill('user@example.com');

// Checking
await page.check('input[type="checkbox"]');
// or
await page.getByLabel('I agree').check();

// Selecting
await page.selectOption('select#country', 'US');
// or
await page.getByLabel('Country').selectOption('United States');

// Pressing keys
await page.press('input', 'Enter');
await page.press('body', 'Escape');
```

### Assertions

Built-in assertions with auto-waiting and retrying:

```typescript
// Element assertions
await expect(page.getByRole('heading')).toHaveText('Welcome');
await expect(page.getByRole('button')).toBeEnabled();
await expect(page.getByRole('textbox')).toHaveValue('');

// Page assertions
await expect(page).toHaveURL(/.*\/dashboard/);
await expect(page).toHaveTitle(/.*Example.*/);
await expect(page).toHaveScreenshot('landing-page.png');

// Element count and visibility
await expect(page.getByRole('button')).toHaveCount(3);
await expect(page.getByRole('alert')).toBeVisible();

// API response assertions
const [response] = await Promise.all([
  page.waitForResponse('*/api/users'),
  page.click('#load-users')
]);
await expect(response).toBeOK();
const json = await response.json();
expect(json.users).toHaveLength(5);
```

## Test Isolation and Setup

### Browser Contexts

Each test gets an isolated browser context:

```typescript
import { test, expect } from '@playwright/test';

test.describe('Authentication flow', () => {
  // Each test gets a fresh context
  test('should login with valid credentials', async ({ page }) => {
    await page.goto('/login');
    await page.getByLabel('Email').fill('user@test.com');
    await page.getByLabel('Password').fill('secret123');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();
  });

  test('should show error for invalid credentials', async ({ page }) => {
    await page.goto('/login');
    await page.getByLabel('Email').fill('wrong@test.com');
    await page.getByLabel('Password').fill('wrongpass');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByText('Invalid credentials')).toBeVisible();
  });
});
```

### Fixtures and Setup

Extend test fixtures for reusable setup:

```typescript
// tests/fixtures.auth.ts
import { test as base } from '@playwright/test';
import type { Page } from '@playwright/test';

export const test = base.extend<{
  authenticatedPage: Page;
}>({
  authenticatedPage: async ({ browser }, use) => {
    const context = await browser.newContext();
    const page = await context.newPage();
    
    // Perform login
    await page.goto('https://app.example.com/login');
    await page.getByLabel('Email').fill('test@example.com');
    await page.getByLabel('Password').fill('password123');
    await page.getByRole('button', { name: 'Sign in' }).click();
    
    // Wait for dashboard to confirm login
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();
    
    await use(page);
    
    // Cleanup
    await context.close();
  },
});

export { expect } from '@playwright/test';
```

Usage in tests:

```typescript
import { test, expect } from './fixtures.auth';

test('should access protected resource', async ({ authenticatedPage }) => {
  await authenticatedPage.goto('/api/profile');
  const response = await authenticatedPage.request.get('/api/profile');
  expect(response.ok()).toBeTruthy();
});
```

## Advanced Features

### Network Interception

Mock API responses for testing edge cases:

```typescript
import { test, expect } from '@playwright/test';

test('should handle API error gracefully', async ({ page }) => {
  // Intercept and mock API response
  await page.route('*/api/user/profile', async route => {
    await route.fulfill({
      status: 500,
      body: JSON.stringify({ error: 'Internal server error' })
    });
  });
  
  await page.goto('/profile');
  await expect(page.getByText('Something went wrong')).toBeVisible();
});
```

### File Downloads and Uploads

Handle file operations safely:

```typescript
import { test, expect } from '@playwright/test';

test('should allow file upload', async ({ page }) => {
  await page.goto('/upload');
  
  // Wait for download promise before triggering download
  const [download] = await Promise.all([
    page.waitForEvent('download'),  // Start waiting for download
    page.click('#export-report')    // Trigger download
  ]);
  
  // Save downloaded file
  await download.saveAs(`./downloads/${download.suggestedFilename()}`);
  expect(download.suggestedFilename()).toContain('report');
});

test('should upload file', async ({ page }) => {
  await page.goto('/upload');
  await page.setInputFiles('input[type="file"]', './test-files/image.png');
  await page.getByRole('button', { name: 'Upload' }).click();
  await expect(page.getByText('Upload successful')).toBeVisible();
});
```

### Multiple Tabs and Popups

Handle multiple browser contexts:

```typescript
import { test, expect } from '@playwright/test';

test('should handle external links', async ({ page }) => {
  await page.goto('https://example.com');
  
  // Wait for new page when clicking external link
  const [newPage] = await Promise.all([
    page.waitForEvent('popup'),           // Wait for popup
    page.click('text=Privacy Policy')     // Click link that opens popup
  ]);
  
  await expect(newPage).toHaveURL(/.*privacy/);
  await expect(newPage.getByRole('heading')).toContainText('Privacy Policy');
  
  await newPage.close();
});
```

### Frames and Shadow DOM

Work with iframes and shadow DOM:

```typescript
import { test, expect } from '@playwright/test';

test('should interact with iframe content', async ({ page }) => {
  await page.goto('/page-with-iframe');
  
  // Get the iframe
  const frame = page.frameLocator('iframe#widget');
  
  // Interact with elements inside the iframe
  await frame.getByLabel('Search').fill('playwright');
  await frame.getByRole('button', { name: 'Search' }).click();
  
  await expect(frame.getByText('Results for playwright')).toBeVisible();
});
```

## Debugging and Troubleshooting

### Trace Viewer

Capture and examine traces for failed tests:

```bash
# Run with tracing
npx playwright test --trace on

# View latest trace
npx playwright show-trace
```

### Console and Error Handling

Monitor console messages and errors:

```typescript
import { test, expect } from '@playwright/test';

test('should log no errors to console', async ({ page }) => {
  const errors: string[] = [];
  
  // Collect console errors
  page.on('console', msg => {
    if (msg.type() === 'error') {
      errors.push(msg.text());
    }
  });
  
  // Collect page errors
  page.on('pageerror', err => {
    errors.push(err.message);
  });
  
  await page.goto('/');
  await expect(page.getByRole('heading')).toHaveText('Home');
  
  // Assert no errors occurred
  expect(errors).toEqual([]);
});
```

### Performance Metrics

Measure and assert performance characteristics:

```typescript
import { test, expect } from '@playwright/test';

test('should load homepage quickly', async ({ page }) => {
  await page.goto('/');
  
  // Measure performance
  const navigationTiming = await page.evaluate(() => {
    const perf = window.performance.timing;
    return {
      loadTime: perf.loadEventEnd - perf.navigationStart,
      domReady: perf.domComplete - perf.navigationStart,
      firstPaint: perf.navigationStart > 0 ? 
        (window.performance.getEntriesByType('paint')[0]?.startTime || 0) : 0
    };
  });
  
  // Assert performance thresholds
  expect(navigationTiming.loadTime).toBeLessThan(3000); // 3 seconds
  expect(navigationTiming.domReady).toBeLessThan(2000); // 2 seconds
});
```

## Best Practices

### Test Data Management

Use isolated test data:

```typescript
import { test, expect } from '@playwright/test';

test('should create and delete test user', async ({ page }) => {
  // Generate unique test data
  const timestamp = Date.now();
  const testUser = {
    email: `test${timestamp}@example.com`,
    password: 'TempPass123!',
    name: `Test User ${timestamp}`
  };
  
  // Create user via API (bypass UI for setup)
  const apiRequest = await page.request.fetch('/api/users', {
    method: 'POST',
    data: testUser
  });
  expect(apiRequest.ok()).toBeTruthy();
  
  // Test login with created user
  await page.goto('/login');
  await page.getByLabel('Email').fill(testUser.email);
  await page.getByLabel('Password').fill(testUser.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByText(`Hello, ${testUser.name}`)).toBeVisible();
  
  // Cleanup: Delete test user
  const deleteRequest = await page.request.fetch(`/api/users/${testUser.email}`, {
    method: 'DELETE'
  });
  expect(deleteRequest.ok()).toBeTruthy();
});
```

### Environment Configuration

Manage different environments safely:

```typescript
import { test, expect } from '@playwright/test';

// Get environment from process.env or config
const baseURL = process.env.TEST_ENV === 'staging' 
  ? 'https://staging.example.com'
  : process.env.TEST_ENV === 'production'
    ? 'https://www.example.com'
    : 'http://localhost:3000';

test.use({ baseURL });

test('should work in any environment', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('heading')).toContainText('Example');
});
```

### Accessibility Testing

Integrate accessibility checks:

```typescript
import { test, expect } from '@playwright/test';
import { axe, toHaveNoViolations } from 'jest-axe';

expect.extend(toHaveNoViolations);

test('should have no accessibility violations', async ({ page }) => {
  await page.goto('/');
  await expect(page).toHaveNoViolations();
});
```

### Visual Regression Testing

Use screenshots for visual validation:

```typescript
import { test, expect } from '@playwright/test';

test('should maintain visual consistency', async ({ page }) => {
  await page.goto('/dashboard');
  
  // Compare against baseline screenshot
  await expect(page).toHaveScreenshot('dashboard-baseline.png', {
    maxDiffPixels: 10,
  });
});
```

## Anti-Patterns to Avoid

|| Anti-Pattern | Why It Fails | Do Instead |
|-------------|-------------|-----------|-----------|
| Using brittle selectors (e.g., `#app > div > div > button:nth-child(3)`) | Breaks on minor UI changes | Use semantic locators (getByRole, getByLabel) |
| Hard-coded waits (`await page.waitForTimeout(1000)`) | Flaky, slow, unreliable | Use built-in waiting or explicit waitFor selectors |
| Sharing state between tests | Order-dependent, flaky tests | Each test gets clean context; use fixtures for setup |
| Testing implementation details | Breaks on refactor | Test user-visible behavior and outcomes |
| Ignoring test isolation | False positives/negatives | Use browser contexts and pages per test |
| Overusing screenshot comparisons | Maintenance overhead, false failures | Reserve for critical UI components only |
| Storing secrets in tests | Security risk | Use environment variables or secure vaults |
| Not cleaning up test data | Polluted environments, conflicting tests | Clean up after yourself via API or UI |
| Disabling security features | Vulnerable to attacks | Keep same-site cookies, CSP, etc. enabled |
| Long, complex tests | Hard to debug, low maintainability | Split into focused, atomic tests |

## Quick Reference

```
SETUP:
  npm init playwright@latest
  npx playwright install
  npx playwright test    # Run all tests
  npx playwright test --headed  # See browser action
  npx playwright test --debug   # Debug mode

TEST STRUCTURE:
  import { test, expect } from '@playwright/test';
  
  test('description', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('heading')).toHaveText('Expected');
  });

LOCATORS:
  page.getByRole('button', { name: 'Submit' })
  page.getByLabel('Email address')
  page.getByPlaceholder('Enter password')
  page.getByText('Welcome user')
  page.getByTestId('nav-menu')

ACTIONS:
  await page.goto(url)
  await page.click(selector)
  await page.fill(selector, 'text')
  await page.check(selector)
  await page.selectOption(selector, 'value')
  await page.press(selector, 'Key')

ASSERTIONS:
  await expect(page).toHaveURL(/.*\/login/)
  await expect(page).toHaveTitle(/.*App.*/)
  await expect(page.getByRole('heading')).toHaveText('Welcome')
  await expect(page.getByRole('textbox')).toHaveValue('email@test.com')
  await expect(page.getByRole('button')).toBeEnabled()
  await expect(page.getByRole('alert')).toBeVisible()
  await expect(page).toHaveScreenshot('file.png')

NETWORK:
  await page.route('*/api/*', route => route.fulfill({status: 200, body: '{}"}'}))
  const [response] = await Promise.all([
    page.waitForResponse('*/api/data'),
    page.click('#load-data')
  ]);

ISOLATION:
  // Each test gets fresh context automatically
  // Use test.extend() for custom fixtures
```