import { expect, test } from '@playwright/test';

const isolatedPreviewURL = (token: string) => `http://${token}.localhost:3000/p/${token}/`;
const previewDocument = '<main id="preview-status">Waiting for storage</main><script>localStorage.setItem("preview-ready", "true");document.getElementById("preview-status").textContent=localStorage.getItem("preview-ready")==="true"?"Preview storage ready":"Preview storage unavailable"</script>';

test('shows the project list as the authenticated home page', async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem('access_token', 'test-access');
    localStorage.setItem('refresh_token', 'test-refresh');
    localStorage.setItem('agentland.locale', 'en-US');
  });
  await page.route('**/api/v1/auth/me', (route) => route.fulfill({ json: { msg: 'ok', code: 200, data: { id: 'user-1', name: 'Ada', email: 'ada@example.com' } } }));
  await page.route('**/api/v1/projects', (route) => route.fulfill({ json: { msg: 'ok', code: 200, data: { items: [{ id: 'project-1', name: 'Demo app', status: 'DRAFT', runtime_status: 'active' }] } } }));

  await page.goto('/projects');
  await expect(page.getByRole('heading', { name: 'Projects' })).toBeVisible();
  await expect(page.getByText('Demo app')).toBeVisible();
  const createButton = page.getByRole('button', { name: 'New project' });
  await createButton.click();
  await expect(page.getByRole('dialog', { name: 'New project' })).toBeVisible();
  await expect(page.getByLabel('Project name')).toBeFocused();
  await page.keyboard.press('Shift+Tab');
  await expect(page.getByRole('button', { name: 'Create' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog', { name: 'New project' })).toHaveCount(0);
  await expect(createButton).toBeFocused();
  expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)).toBe(false);
});

test('workspace tabs fit the viewport and open the code editor', async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem('access_token', 'test-access');
    localStorage.setItem('refresh_token', 'test-refresh');
    localStorage.setItem('agentland.locale', 'en-US');
  });
  const envelope = (data: unknown) => ({ msg: 'ok', code: 200, data });
  await page.route('**/api/v1/auth/me', (route) => route.fulfill({ json: envelope({ id: 'user-1', name: 'Ada', email: 'ada@example.com' }) }));
  await page.route('**/api/v1/projects/project-1', (route) => route.fulfill({ json: envelope({ id: 'project-1', name: 'Demo app', status: 'DRAFT', runtime_status: 'active' }) }));
  await page.route('**/api/v1/projects/project-1/messages*', (route) => route.fulfill({ json: envelope({ items: [], next_cursor: null }) }));
  await page.route('**/api/v1/projects/project-1/preview', (route) => route.fulfill({ json: envelope({ status: 'running', port: 3000, preview_url: isolatedPreviewURL('viewport-preview') }) }));
  await page.route('**/p/viewport-preview/**', (route) => route.fulfill({ contentType: 'text/html', body: previewDocument }));
  await page.route('**/api/v1/projects/project-1/files/tree*', (route) => route.fulfill({ json: envelope({ root: '.', nodes: [{ path: 'src/App.tsx', name: 'App.tsx', type: 'file' }] }) }));
  await page.route('**/api/v1/projects/project-1/files/content*', (route) => route.fulfill({ json: envelope({ path: 'src/App.tsx', content: 'export default function App() {}', sha: 'sha-1' }) }));
  await page.route('**/api/v1/projects/project-1/publications', (route) => route.fulfill({ json: envelope({ items: [{
    id: 'pub-1', project_id: 'project-1', status: 'completed', context: '.', dockerfile: 'Dockerfile',
    image_ref: 'registry.example/apps/project-1:pub-1', digest: `sha256:${'a'.repeat(64)}`,
    logs: 'build complete', created_at: '2026-08-02T10:00:00Z',
  }] }) }));

  await page.goto('/projects/project-1');
  await expect(page.getByText('Demo app')).toBeVisible();
  await page.getByRole('tab', { name: 'Preview' }).click();
  await expect(page.getByRole('group', { name: 'Preview viewport' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Desktop' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Tablet' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Mobile' })).toBeVisible();
  await page.getByRole('button', { name: 'Tablet' }).click();
  await expect.poll(async () => Math.round((await page.getByTitle('Application preview').boundingBox())?.width ?? 0)).toBe(768);
  await expect(page.frameLocator('iframe[title="Application preview"]').getByText('Preview storage ready')).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)).toBe(false);
  await page.getByRole('tab', { name: 'Code' }).click();
  await expect(page.getByRole('region', { name: 'Code' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'App.tsx' })).toBeVisible();
  const hasHorizontalOverflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
  expect(hasHorizontalOverflow).toBe(false);
  await page.getByRole('tab', { name: 'Publish' }).click();
  await expect(page.getByText('Container image')).toBeVisible();
  await expect(page.getByText(/registry\.example\/apps\/project-1@sha256:/)).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)).toBe(false);
});

test('runs the agent, saves code, and starts a sandboxed preview', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-1440', 'Full workflow runs once at the desktop acceptance viewport.');
  await page.addInitScript(() => {
    localStorage.setItem('access_token', 'test-access');
    localStorage.setItem('refresh_token', 'test-refresh');
    localStorage.setItem('agentland.locale', 'en-US');
  });
  const envelope = (data: unknown, code = 200) => ({ msg: 'ok', code, data });
  let idempotencyKey = '';
  let savedContent = '';
  let previewPort = 0;
  let previewRunning = false;

  await page.route('**/api/v1/auth/me', (route) => route.fulfill({ json: envelope({ id: 'user-1', name: 'Ada', email: 'ada@example.com' }) }));
  await page.route('**/api/v1/projects/project-1', (route) => route.fulfill({ json: envelope({ id: 'project-1', name: 'Demo app', status: 'DRAFT', runtime_status: 'active' }) }));
  await page.route('**/api/v1/projects/project-1/messages*', (route) => route.fulfill({ json: envelope({
    items: [
      { id: 'old-user', role: 'user', content: 'Create the initial app', status: 'completed', created_at: '2026-08-02T09:00:00Z' },
      { id: 'old-assistant', role: 'assistant', content: 'The initial app is ready.', status: 'completed', created_at: '2026-08-02T09:01:00Z' },
    ],
    next_cursor: null,
  }) }));
  await page.route('**/api/v1/projects/project-1/runs', async (route) => {
    idempotencyKey = route.request().headers()['idempotency-key'] ?? '';
    await route.fulfill({ status: 202, json: envelope({ run_id: 'run-new', user_message_id: 'new-user', status: 'queued' }, 202) });
  });
  await page.route('**/api/v1/runs/run-new/events', (route) => {
    const event = (type: string, sequence: number, payload: Record<string, unknown>) => `id: ${sequence}-0\nevent: ${type}\ndata: ${JSON.stringify({ type, run_id: 'run-new', conversation_id: 'conversation-1', sequence, timestamp: '2026-08-02T10:00:00Z', payload })}\n\n`;
    return route.fulfill({
      contentType: 'text/event-stream',
      body: [
        event('run.started', 1, {}),
        event('tool.started', 2, { tool_call_id: 'tool-1', name: 'write_file' }),
        event('tool.output', 3, { tool_call_id: 'tool-1', output: 'src/App.tsx' }),
        event('tool.completed', 4, { tool_call_id: 'tool-1' }),
        event('message.delta', 5, { delta: 'Implemented the requested change.' }),
        event('run.completed', 6, {}),
      ].join(''),
    });
  });
  await page.route('**/api/v1/runs/run-new', (route) => route.fulfill({ json: envelope({ id: 'run-new', project_id: 'project-1', status: 'running', last_sequence: 1, created_at: '2026-08-02T10:00:00Z' }) }));
  await page.route('**/api/v1/projects/project-1/files/tree*', (route) => route.fulfill({ json: envelope({ root: '.', nodes: [{ path: 'src/App.tsx', name: 'App.tsx', type: 'file' }] }) }));
  await page.route('**/api/v1/projects/project-1/files/content*', async (route) => {
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON() as { content: string; sha: string };
      savedContent = body.content;
      await route.fulfill({ json: envelope({ path: 'src/App.tsx', size: body.content.length, sha: 'sha-2' }) });
      return;
    }
    await route.fulfill({ json: envelope({ path: 'src/App.tsx', content: 'export const value = 1;', sha: 'sha-1' }) });
  });
  await page.route('**/api/v1/projects/project-1/previews', async (route) => {
    previewPort = (route.request().postDataJSON() as { port: number }).port;
    previewRunning = true;
    await route.fulfill({ json: envelope({ status: 'running', port: previewPort, preview_url: isolatedPreviewURL('workflow-preview') }) });
  });
  await page.route('**/api/v1/projects/project-1/preview', (route) => route.fulfill({ json: envelope(previewRunning ? { status: 'running', port: previewPort, preview_url: isolatedPreviewURL('workflow-preview') } : { status: 'idle' }) }));
  await page.route('**/p/workflow-preview/**', (route) => route.fulfill({ contentType: 'text/html', body: previewDocument }));

  await page.goto('/projects/project-1');
  await expect(page.getByText('Create the initial app')).toBeVisible();
  await expect(page.getByText('The initial app is ready.')).toBeVisible();
  await page.getByPlaceholder('Describe the change you want...').fill('Update the title');
  await page.getByRole('button', { name: 'Send' }).click();
  await expect(page.getByText('Implemented the requested change.')).toBeVisible();
  await expect(page.getByText('write_file')).toBeVisible();
  expect(idempotencyKey).not.toBe('');

  await page.getByRole('tab', { name: 'Code' }).click();
  await expect(page.getByRole('button', { name: 'App.tsx' })).toBeVisible();
  await page.locator('.monaco-editor').click();
  await page.keyboard.press('ControlOrMeta+A');
  await page.keyboard.insertText('export const saved = true;');
  await page.getByRole('tab', { name: 'Preview' }).click();
  await page.getByRole('tab', { name: 'Code' }).click();
  await page.getByTitle('Save').click();
  await expect.poll(() => savedContent).toContain('saved = true');

  await page.getByRole('tab', { name: 'Preview' }).click();
  await page.getByRole('button', { name: 'Start preview' }).click();
  await expect.poll(() => previewPort).toBe(3000);
  await expect(page.getByTitle('Application preview')).toBeVisible();
  await expect(page.getByTitle('Application preview')).toHaveAttribute('sandbox', /allow-scripts/);
  await expect(page.getByTitle('Application preview')).toHaveAttribute('sandbox', /allow-same-origin/);
  await expect(page.frameLocator('iframe[title="Application preview"]').getByText('Preview storage ready')).toBeVisible();
});

test('replays and cancels an active run after refresh', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-1440', 'Run recovery runs once at the desktop acceptance viewport.');
  await page.addInitScript(() => {
    localStorage.setItem('access_token', 'test-access');
    localStorage.setItem('refresh_token', 'test-refresh');
    localStorage.setItem('agentland.locale', 'en-US');
  });
  const envelope = (data: unknown) => ({ msg: 'ok', code: 200, data });
  let active = false;
  let cancelled = false;
  await page.route('**/api/v1/auth/me', (route) => route.fulfill({ json: envelope({ id: 'user-1', name: 'Ada', email: 'ada@example.com' }) }));
  await page.route('**/api/v1/projects/project-1', (route) => route.fulfill({ json: envelope({ id: 'project-1', name: 'Recovery app', status: 'DRAFT', runtime_status: 'active', active_run_id: active ? 'run-active' : null, last_run_id: 'run-active' }) }));
  await page.route('**/api/v1/projects/project-1/messages*', (route) => route.fulfill({ json: envelope({ items: [
    { id: 'user-active', run_id: 'run-active', role: 'user', content: 'Continue the work', status: 'completed', created_at: '2026-08-02T10:00:00Z' },
    { id: 'assistant-active', run_id: 'run-active', role: 'assistant', content: 'Work', status: 'streaming', created_at: '2026-08-02T10:00:01Z' },
  ], next_cursor: null }) }));
  await page.route('**/api/v1/runs/run-active/events', (route) => {
    const event = (type: string, payload: Record<string, unknown>) => `event: ${type}\ndata: ${JSON.stringify({ type, run_id: 'run-active', sequence: 0, timestamp: '2026-08-02T10:00:02Z', payload })}\n\n`;
    return route.fulfill({ contentType: 'text/event-stream', body: event('message.delta', { delta: 'Working' }) + event('tool.started', { tool_call_id: 'tool-recovery', name: 'shell' }) });
  });
  await page.route('**/api/v1/runs/run-active', (route) => route.fulfill({ json: envelope({ id: 'run-active', project_id: 'project-1', status: cancelled ? 'cancelled' : 'running', last_sequence: 1, created_at: '2026-08-02T10:00:00Z' }) }));
  await page.route('**/api/v1/runs/run-active/cancel', async (route) => {
    active = false;
    cancelled = true;
    await route.fulfill({ json: envelope({ status: 'cancelled' }) });
  });
  await page.route('**/api/v1/projects/project-1/preview', (route) => route.fulfill({ json: envelope({ status: 'idle' }) }));

  await page.goto('/projects/project-1');
  active = true;
  await page.reload();
  await expect(page.getByText('Working', { exact: true })).toBeVisible();
  await expect(page.getByText('WorkWorking', { exact: true })).toHaveCount(0);
  await expect(page.getByText('shell')).toBeVisible();
  await page.getByRole('button', { name: 'Stop run' }).click();
  await expect.poll(() => cancelled).toBe(true);
  await expect(page.getByText('The run was cancelled')).toBeVisible();
});
