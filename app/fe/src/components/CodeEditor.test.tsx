import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { I18nProvider } from '../i18n';
import { queryKeys } from '../queryKeys';
import { fileDraftKey, useWorkspaceStore, WORKSPACE_DRAFTS_STORAGE_KEY } from '../stores/workspace';
import { server } from '../test/server';
import CodeEditor from './CodeEditor';

vi.mock('@monaco-editor/react', () => ({
  default: ({ value, onChange }: { value: string; onChange: (value: string) => void }) => (
    <textarea aria-label="code editor" value={value} onChange={(event) => onChange(event.target.value)} />
  ),
}));

function envelope<T>(data: T, status = 200) {
  return HttpResponse.json({ msg: status === 200 ? 'ok' : 'conflict', code: status, data }, { status });
}

describe('CodeEditor', () => {
  beforeEach(() => {
    localStorage.setItem('agentland.locale', 'en-US');
    useWorkspaceStore.setState({ fileDrafts: {} });
    useWorkspaceStore.getState().reset();
  });

  it('surfaces a SHA conflict and can overwrite using the latest SHA', async () => {
    let currentSha = 'sha-1';
    let saveAttempts = 0;
    server.use(
      http.get('/api/v1/projects/project-1/files/tree', () => envelope({ root: '.', nodes: [{ path: 'src/App.tsx', name: 'App.tsx', type: 'file' }] })),
      http.get('/api/v1/projects/project-1/files/content', () => envelope({ path: 'src/App.tsx', content: 'old', sha: currentSha })),
      http.put('/api/v1/projects/project-1/files/content', async ({ request }) => {
        const body = await request.json() as { content: string; sha: string };
        saveAttempts += 1;
        if (saveAttempts === 1) {
          currentSha = 'sha-2';
          return envelope({ type: 'FILE_CONFLICT' }, 409);
        }
        expect(body.sha).toBe('sha-2');
        return envelope({ path: 'src/App.tsx', content: body.content, sha: 'sha-3' });
      }),
    );

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <I18nProvider><CodeEditor projectId="project-1" readOnly={false} /></I18nProvider>
      </QueryClientProvider>,
    );

    const user = userEvent.setup();
    const editor = await screen.findByLabelText('code editor');
    await user.clear(editor);
    await user.type(editor, 'new');
    await user.click(screen.getByTitle('Save'));
    expect(await screen.findByText(/This file changed on the server/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Overwrite latest' }));
    await waitFor(() => expect(saveAttempts).toBe(2));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Saved'));
  });

  it('recreates a file with an explicit empty SHA when the agent deleted it', async () => {
    let reads = 0;
    let saveAttempts = 0;
    server.use(
      http.get('/api/v1/projects/project-1/files/tree', () => envelope({ root: '.', nodes: [{ path: 'src/App.tsx', name: 'App.tsx', type: 'file' }] })),
      http.get('/api/v1/projects/project-1/files/content', () => {
        reads += 1;
        if (reads === 1) return envelope({ path: 'src/App.tsx', content: 'old', sha: 'sha-1' });
        return HttpResponse.json({ msg: 'not found', code: 404, data: { type: 'NOT_FOUND' } }, { status: 404 });
      }),
      http.put('/api/v1/projects/project-1/files/content', async ({ request }) => {
        const body = await request.json() as { content: string; sha: string };
        saveAttempts += 1;
        if (saveAttempts === 1) return envelope({ type: 'FILE_CONFLICT', sha: '' }, 409);
        expect(body).toEqual({ content: 'restored', sha: '' });
        return envelope({ path: 'src/App.tsx', sha: 'sha-new' });
      }),
    );

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <I18nProvider><CodeEditor projectId="project-1" readOnly={false} /></I18nProvider>
      </QueryClientProvider>,
    );

    const user = userEvent.setup();
    const editor = await screen.findByLabelText('code editor');
    await user.clear(editor);
    await user.type(editor, 'restored');
    await user.click(screen.getByTitle('Save'));
    expect(await screen.findByText(/This file changed on the server/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Overwrite latest' }));
    await waitFor(() => expect(saveAttempts).toBe(2));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Saved'));
  });

  it('keeps a separate unsaved draft while switching files and remounting', async () => {
    server.use(
      http.get('/api/v1/projects/project-1/files/tree', () => envelope({ root: '.', nodes: [
        { path: 'src/App.tsx', name: 'App.tsx', type: 'file' },
        { path: 'README.md', name: 'README.md', type: 'file' },
      ] })),
      http.get('/api/v1/projects/project-1/files/content', ({ request }) => {
        const path = new URL(request.url).searchParams.get('path');
        return envelope(path === 'README.md'
          ? { path, content: '# Readme', sha: 'readme-sha' }
          : { path, content: 'server app', sha: 'app-sha' });
      }),
    );

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const view = render(
      <QueryClientProvider client={queryClient}>
        <I18nProvider><CodeEditor projectId="project-1" readOnly={false} /></I18nProvider>
      </QueryClientProvider>,
    );
    const user = userEvent.setup();
    const editor = await screen.findByLabelText('code editor');
    await user.clear(editor);
    await user.type(editor, 'local app draft');

    await user.click(screen.getByRole('button', { name: 'README.md' }));
    await waitFor(() => expect(screen.getByLabelText('code editor')).toHaveValue('# Readme'));
    await user.click(screen.getByRole('button', { name: /App.tsx/ }));
    await waitFor(() => expect(screen.getByLabelText('code editor')).toHaveValue('local app draft'));

    view.unmount();
    render(
      <QueryClientProvider client={queryClient}>
        <I18nProvider><CodeEditor projectId="project-1" readOnly={false} /></I18nProvider>
      </QueryClientProvider>,
    );
    expect(await screen.findByLabelText('code editor')).toHaveValue('local app draft');
    expect(screen.getByRole('status')).toHaveTextContent('Unsaved changes');
  });

  it('preserves the local draft when the server changes until the user resolves it', async () => {
    let remote = { content: 'server v1', sha: 'sha-1' };
    server.use(
      http.get('/api/v1/projects/project-1/files/tree', () => envelope({ root: '.', nodes: [{ path: 'src/App.tsx', name: 'App.tsx', type: 'file' }] })),
      http.get('/api/v1/projects/project-1/files/content', () => envelope({ path: 'src/App.tsx', ...remote })),
    );

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const renderEditor = () => render(
      <QueryClientProvider client={queryClient}>
        <I18nProvider><CodeEditor projectId="project-1" readOnly={false} /></I18nProvider>
      </QueryClientProvider>,
    );
    const view = renderEditor();
    const user = userEvent.setup();
    const editor = await screen.findByLabelText('code editor');
    await user.clear(editor);
    await user.type(editor, 'local draft');
    remote = { content: 'server v2', sha: 'sha-2' };
    await queryClient.invalidateQueries({ queryKey: queryKeys.file('project-1', 'src/App.tsx') });
    expect(await screen.findByText(/Your local draft is preserved/)).toBeInTheDocument();
    expect(screen.getByLabelText('code editor')).toHaveValue('local draft');

    await user.click(screen.getByRole('button', { name: 'Reload server version' }));
    await waitFor(() => expect(screen.getByLabelText('code editor')).toHaveValue('server v2'));
    expect(screen.queryByText(/Your local draft is preserved/)).not.toBeInTheDocument();
  });

  it('persists drafts across workspace resets and store hydration', async () => {
    const draft = { content: 'local draft', baseContent: 'server', baseSha: 'sha-1' };
    useWorkspaceStore.getState().setFileDraft('project-1', 'src/App.tsx', draft);

    const serialized = sessionStorage.getItem(WORKSPACE_DRAFTS_STORAGE_KEY);
    expect(serialized).toContain('local draft');
    useWorkspaceStore.getState().reset();
    expect(useWorkspaceStore.getState().fileDrafts[fileDraftKey('project-1', 'src/App.tsx')]).toEqual(draft);

    useWorkspaceStore.setState({ fileDrafts: {} });
    sessionStorage.setItem(WORKSPACE_DRAFTS_STORAGE_KEY, serialized!);
    await useWorkspaceStore.persist.rehydrate();
    expect(useWorkspaceStore.getState().fileDrafts[fileDraftKey('project-1', 'src/App.tsx')]).toEqual(draft);
  });

  it('keeps edits made while an earlier save is in flight', async () => {
    let releaseFirstSave = () => {};
    const firstSave = new Promise<void>((resolve) => { releaseFirstSave = resolve; });
    let saveAttempts = 0;
    server.use(
      http.get('/api/v1/projects/project-1/files/tree', () => envelope({ root: '.', nodes: [{ path: 'src/App.tsx', name: 'App.tsx', type: 'file' }] })),
      http.get('/api/v1/projects/project-1/files/content', () => envelope({ path: 'src/App.tsx', content: 'server', sha: 'sha-1' })),
      http.put('/api/v1/projects/project-1/files/content', async () => {
        saveAttempts += 1;
        if (saveAttempts === 1) await firstSave;
        return envelope({ path: 'src/App.tsx', sha: `sha-${saveAttempts + 1}` });
      }),
    );

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <I18nProvider><CodeEditor projectId="project-1" readOnly={false} /></I18nProvider>
      </QueryClientProvider>,
    );
    const user = userEvent.setup();
    const editor = await screen.findByLabelText('code editor');
    await user.clear(editor);
    await user.type(editor, 'first edit');
    await user.click(screen.getByTitle('Save'));
    await waitFor(() => expect(saveAttempts).toBe(1));

    await user.clear(editor);
    await user.type(editor, 'newer edit');
    releaseFirstSave();

    await waitFor(() => expect(screen.getByLabelText('code editor')).toHaveValue('newer edit'));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Unsaved changes'));
    expect(screen.getByTitle('Save')).toBeEnabled();
  });
});
