import { QueryClient, QueryClientProvider, QueryObserver } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it, vi } from 'vitest';
import * as api from '../api';
import { I18nProvider } from '../i18n';
import { queryKeys } from '../queryKeys';
import { useAuthStore } from '../stores/auth';
import { server } from '../test/server';
import ChatPanel from './ChatPanel';

function envelope<T>(data: T, status = 200) {
  return HttpResponse.json({ msg: 'ok', code: status, data }, { status });
}

function renderPanel(queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })) {
  return { queryClient, ...render(
    <QueryClientProvider client={queryClient}>
      <I18nProvider>
        <ChatPanel project={{ id: 'project-1', name: 'Demo', status: 'DRAFT', runtime_status: 'active' }} readOnly={false} />
      </I18nProvider>
    </QueryClientProvider>,
  ) };
}

describe('ChatPanel', () => {
  it('renders message deltas and tool progress from the run event stream', async () => {
    localStorage.setItem('agentland.locale', 'en-US');
    useAuthStore.getState().setSession({ accessToken: 'access-token', refreshToken: 'refresh-token', user: null });
    let idempotencyKey = '';
    const encoder = new TextEncoder();
    const runEvent = (type: string, sequence: number, payload: Record<string, unknown>) => JSON.stringify({
      type,
      run_id: 'run-1',
      conversation_id: 'conversation-1',
      sequence,
      timestamp: '2026-08-02T10:00:00Z',
      payload,
    });

    server.use(
      http.get('/api/v1/projects/project-1/messages', () => envelope({ items: [], next_cursor: null })),
      http.post('/api/v1/projects/project-1/runs', ({ request }) => {
        idempotencyKey = request.headers.get('Idempotency-Key') ?? '';
        return envelope({ run_id: 'run-1', user_message_id: 'message-1', status: 'running' }, 202);
      }),
      http.get('/api/v1/runs/run-1/events', () => {
        const body = [
          `event: run.started\ndata: ${runEvent('run.started', 1, {})}\n\n`,
          `event: tool.started\ndata: ${runEvent('tool.started', 2, { tool_call_id: 'tool-1', name: 'write_file' })}\n\n`,
          `event: tool.output\ndata: ${runEvent('tool.output', 3, { tool_call_id: 'tool-1', output: 'src/App.tsx' })}\n\n`,
          `event: tool.completed\ndata: ${runEvent('tool.completed', 4, { tool_call_id: 'tool-1' })}\n\n`,
          `event: message.delta\ndata: ${runEvent('message.delta', 5, { delta: 'Done' })}\n\n`,
          `event: run.completed\ndata: ${runEvent('run.completed', 6, {})}\n\n`,
        ].join('');
        return new HttpResponse(new ReadableStream({ start(controller) { controller.enqueue(encoder.encode(body)); controller.close(); } }), { headers: { 'Content-Type': 'text/event-stream' } });
      }),
      http.get('/api/v1/runs/run-1', () => envelope({ id: 'run-1', project_id: 'project-1', status: 'running', last_sequence: 1, created_at: '2026-08-02T10:00:00Z' })),
      http.get('/api/v1/projects/project-1', () => envelope({ id: 'project-1', name: 'Demo', status: 'DRAFT', runtime_status: 'active' })),
      http.get('/api/v1/projects/project-1/files/tree', () => envelope({ root: '.', nodes: [] })),
    );

    renderPanel();
    const user = userEvent.setup();
    await user.type(await screen.findByPlaceholderText('Describe the change you want...'), 'Build it');
    fireEvent.click(screen.getByRole('button', { name: 'Send' }));

    expect(await screen.findByText('Done')).toBeInTheDocument();
    expect(screen.getByText('write_file')).toBeInTheDocument();
    expect(screen.getByText('src/App.tsx')).toBeInTheDocument();
    await waitFor(() => expect(idempotencyKey).not.toBe(''));
  });

  it('replays an active run from the beginning without duplicating persisted partial text', async () => {
    localStorage.setItem('agentland.locale', 'en-US');
    useAuthStore.getState().setSession({ accessToken: 'access-token', refreshToken: 'refresh-token', user: null });
    const encoder = new TextEncoder();
    const runEvent = (type: string, sequence: number, payload: Record<string, unknown>) => JSON.stringify({
      type,
      run_id: 'run-active',
      sequence,
      timestamp: '2026-08-02T10:00:00Z',
      payload,
    });
    server.use(
      http.get('/api/v1/projects/project-1/messages', () => envelope({
        items: [{ id: 'assistant-1', run_id: 'run-active', role: 'assistant', content: 'Hel', status: 'streaming', created_at: '2026-08-02T10:00:00Z' }],
        next_cursor: null,
      })),
      http.get('/api/v1/runs/run-active/events', () => {
        const body = [
          `event: message.delta\ndata: ${runEvent('message.delta', 0, { delta: 'Hel' })}\n\n`,
          `event: message.delta\ndata: ${runEvent('message.delta', 1, { delta: 'lo' })}\n\n`,
          `event: run.completed\ndata: ${runEvent('run.completed', 2, {})}\n\n`,
        ].join('');
        return new HttpResponse(new ReadableStream({ start(controller) { controller.enqueue(encoder.encode(body)); controller.close(); } }), { headers: { 'Content-Type': 'text/event-stream' } });
      }),
      http.get('/api/v1/runs/run-active', () => envelope({ id: 'run-active', project_id: 'project-1', status: 'running', last_sequence: 1, created_at: '2026-08-02T10:00:00Z' })),
      http.get('/api/v1/projects/project-1', () => envelope({ id: 'project-1', name: 'Demo', status: 'DRAFT', runtime_status: 'active' })),
      http.get('/api/v1/projects/project-1/files/tree', () => envelope({ root: '.', nodes: [] })),
    );

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <I18nProvider>
          <ChatPanel project={{ id: 'project-1', name: 'Demo', status: 'DRAFT', runtime_status: 'active', active_run_id: 'run-active' }} readOnly={false} />
        </I18nProvider>
      </QueryClientProvider>,
    );

    expect(await screen.findByText('Hello')).toBeInTheDocument();
    expect(screen.queryByText('HelHello')).not.toBeInTheDocument();
  });

  it('keeps the persisted assistant prefix when recovery starts with newer deltas', async () => {
    localStorage.setItem('agentland.locale', 'en-US');
    useAuthStore.getState().setSession({ accessToken: 'access-token', refreshToken: 'refresh-token', user: null });
    const encoder = new TextEncoder();
    const runEvent = (type: string, sequence: number, payload: Record<string, unknown>) => JSON.stringify({
      type,
      run_id: 'run-resume',
      sequence,
      timestamp: '2026-08-02T10:00:00Z',
      payload,
    });
    server.use(
      http.get('/api/v1/projects/project-1/messages', () => envelope({
        items: [{ id: 'assistant-resume', run_id: 'run-resume', role: 'assistant', content: 'Hello', status: 'pending', created_at: '2026-08-02T10:00:00Z' }],
        next_cursor: null,
      })),
      http.get('/api/v1/runs/run-resume/events', () => {
        const body = [
          `event: message.delta\ndata: ${runEvent('message.delta', 6, { content: ' world' })}\n\n`,
          `event: run.completed\ndata: ${runEvent('run.completed', 7, {})}\n\n`,
        ].join('');
        return new HttpResponse(new ReadableStream({ start(controller) { controller.enqueue(encoder.encode(body)); controller.close(); } }), { headers: { 'Content-Type': 'text/event-stream' } });
      }),
      http.get('/api/v1/runs/run-resume', () => envelope({ id: 'run-resume', project_id: 'project-1', status: 'running', last_sequence: 5, created_at: '2026-08-02T10:00:00Z' })),
      http.get('/api/v1/projects/project-1', () => envelope({ id: 'project-1', name: 'Demo', status: 'DRAFT', runtime_status: 'active' })),
      http.get('/api/v1/projects/project-1/files/tree', () => envelope({ root: '.', nodes: [] })),
    );

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <I18nProvider>
          <ChatPanel project={{ id: 'project-1', name: 'Demo', status: 'DRAFT', runtime_status: 'active', active_run_id: 'run-resume' }} readOnly={false} />
        </I18nProvider>
      </QueryClientProvider>,
    );

    expect(await screen.findByText('Hello world')).toBeInTheDocument();
    expect(screen.queryByText(' world')).not.toBeInTheDocument();
  });

  it('reconciles terminal run state when the event stream ends without a terminal event', async () => {
    localStorage.setItem('agentland.locale', 'en-US');
    server.use(http.get('/api/v1/projects/project-1/messages', () => envelope({ items: [], next_cursor: null })));
    const streamSpy = vi.spyOn(api, 'streamRunEvents').mockRejectedValueOnce(new Error('stream closed'));
    const runSpy = vi.spyOn(api, 'getRun').mockResolvedValueOnce({
      id: 'run-terminal',
      project_id: 'project-1',
      status: 'completed',
      last_sequence: 4,
      created_at: '2026-08-02T10:00:00Z',
      completed_at: '2026-08-02T10:01:00Z',
    });

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <I18nProvider>
          <ChatPanel project={{ id: 'project-1', name: 'Demo', status: 'DRAFT', runtime_status: 'active', active_run_id: 'run-terminal' }} readOnly={false} />
        </I18nProvider>
      </QueryClientProvider>,
    );

    await waitFor(() => expect(runSpy).toHaveBeenCalledWith('run-terminal'));
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Stop run' })).not.toBeInTheDocument());
    expect(screen.queryByText('The live event stream disconnected.')).not.toBeInTheDocument();
    streamSpy.mockRestore();
    runSpy.mockRestore();
  });

  it('keeps the run active while cancellation is only requested', async () => {
    localStorage.setItem('agentland.locale', 'en-US');
    server.use(
      http.get('/api/v1/projects/project-1/messages', () => envelope({ items: [], next_cursor: null })),
      http.get('/api/v1/runs/run-cancel', () => envelope({ id: 'run-cancel', project_id: 'project-1', status: 'running', last_sequence: 1, created_at: '2026-08-02T10:00:00Z' })),
      http.post('/api/v1/runs/run-cancel/cancel', () => envelope({ status: 'running' })),
    );
    const streamSpy = vi.spyOn(api, 'streamRunEvents').mockImplementation((_runId, options) => new Promise<void>((resolve) => {
      options.signal.addEventListener('abort', () => resolve(), { once: true });
    }));
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <I18nProvider>
          <ChatPanel project={{ id: 'project-1', name: 'Demo', status: 'DRAFT', runtime_status: 'active', active_run_id: 'run-cancel' }} readOnly={false} />
        </I18nProvider>
      </QueryClientProvider>,
    );

    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Stop run' }));
    const stopping = await screen.findByRole('button', { name: 'Stopping...' });
    expect(stopping).toBeDisabled();
    expect(screen.getByPlaceholderText('Describe the change you want...')).toBeDisabled();
    streamSpy.mockRestore();
  });

  it('keeps polling an active run until the server reports a terminal status', async () => {
    localStorage.setItem('agentland.locale', 'en-US');
    let runReads = 0;
    server.use(
      http.get('/api/v1/projects/project-1/messages', () => envelope({ items: [], next_cursor: null })),
      http.get('/api/v1/runs/run-poll', () => {
        runReads += 1;
        return envelope({
          id: 'run-poll',
          project_id: 'project-1',
          status: runReads > 1 ? 'completed' : 'running',
          last_sequence: runReads,
          created_at: '2026-08-02T10:00:00Z',
        });
      }),
    );
    const streamSpy = vi.spyOn(api, 'streamRunEvents').mockImplementation((_runId, options) => new Promise<void>((resolve) => {
      options.signal.addEventListener('abort', () => resolve(), { once: true });
    }));
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <I18nProvider>
          <ChatPanel project={{ id: 'project-1', name: 'Demo', status: 'DRAFT', runtime_status: 'active', active_run_id: 'run-poll' }} readOnly={false} />
        </I18nProvider>
      </QueryClientProvider>,
    );

    expect(await screen.findByRole('button', { name: 'Stop run' })).toBeInTheDocument();
    await waitFor(() => expect(runReads).toBeGreaterThanOrEqual(2), { timeout: 3_500 });
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Stop run' })).not.toBeInTheDocument());
    expect(screen.getByPlaceholderText('Describe the change you want...')).toBeEnabled();
    streamSpy.mockRestore();
  });

  it('clears local run state when the project no longer has an active run', async () => {
    localStorage.setItem('agentland.locale', 'en-US');
    server.use(
      http.get('/api/v1/projects/project-1/messages', () => envelope({ items: [], next_cursor: null })),
      http.get('/api/v1/runs/run-stale', () => envelope({ id: 'run-stale', project_id: 'project-1', status: 'running', last_sequence: 1, created_at: '2026-08-02T10:00:00Z' })),
    );
    const streamSpy = vi.spyOn(api, 'streamRunEvents').mockImplementation((_runId, options) => new Promise<void>((resolve) => {
      options.signal.addEventListener('abort', () => resolve(), { once: true });
    }));
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const view = render(
      <QueryClientProvider client={queryClient}>
        <I18nProvider>
          <ChatPanel project={{ id: 'project-1', name: 'Demo', status: 'DRAFT', runtime_status: 'active', active_run_id: 'run-stale' }} readOnly={false} />
        </I18nProvider>
      </QueryClientProvider>,
    );
    expect(await screen.findByRole('button', { name: 'Stop run' })).toBeInTheDocument();

    view.rerender(
      <QueryClientProvider client={queryClient}>
        <I18nProvider>
          <ChatPanel project={{ id: 'project-1', name: 'Demo', status: 'DRAFT', runtime_status: 'active', active_run_id: null }} readOnly={false} />
        </I18nProvider>
      </QueryClientProvider>,
    );

    await waitFor(() => expect(screen.queryByRole('button', { name: 'Stop run' })).not.toBeInTheDocument());
    expect(screen.getByPlaceholderText('Describe the change you want...')).toBeEnabled();
    streamSpy.mockRestore();
  });

  it('loads older message pages before the latest page', async () => {
    localStorage.setItem('agentland.locale', 'en-US');
    server.use(http.get('/api/v1/projects/project-1/messages', ({ request }) => {
      const cursor = new URL(request.url).searchParams.get('cursor');
      return cursor === 'older-cursor'
        ? envelope({ items: [{ id: 'old', role: 'user', content: 'Earlier request', created_at: '2026-08-01T10:00:00Z' }], next_cursor: null })
        : envelope({ items: [{ id: 'new', role: 'assistant', content: 'Latest response', created_at: '2026-08-02T10:00:00Z' }], next_cursor: 'older-cursor' });
    }));

    renderPanel();
    const latest = await screen.findByText('Latest response');
    await userEvent.setup().click(screen.getByRole('button', { name: 'Load earlier messages' }));
    const earlier = await screen.findByText('Earlier request');
    expect(earlier.compareDocumentPosition(latest) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('refetches active file content after an agent tool changes files', async () => {
    localStorage.setItem('agentland.locale', 'en-US');
    const encoder = new TextEncoder();
    const runEvent = (type: string, sequence: number, payload: Record<string, unknown>) => JSON.stringify({
      type,
      run_id: 'run-files',
      sequence,
      timestamp: '2026-08-02T10:00:00Z',
      payload,
    });
    server.use(
      http.get('/api/v1/projects/project-1/messages', () => envelope({ items: [], next_cursor: null })),
      http.post('/api/v1/projects/project-1/runs', () => envelope({ run_id: 'run-files', user_message_id: 'message-files', status: 'running' }, 202)),
      http.get('/api/v1/runs/run-files', () => envelope({ id: 'run-files', project_id: 'project-1', status: 'running', last_sequence: 1, created_at: '2026-08-02T10:00:00Z' })),
      http.get('/api/v1/runs/run-files/events', () => {
        const body = [
          `event: tool.started\ndata: ${runEvent('tool.started', 1, { tool_call_id: 'tool-files', name: 'write_file' })}\n\n`,
          `event: tool.completed\ndata: ${runEvent('tool.completed', 2, { tool_call_id: 'tool-files' })}\n\n`,
          `event: run.completed\ndata: ${runEvent('run.completed', 3, {})}\n\n`,
        ].join('');
        return new HttpResponse(new ReadableStream({ start(controller) { controller.enqueue(encoder.encode(body)); controller.close(); } }), { headers: { 'Content-Type': 'text/event-stream' } });
      }),
    );
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const fileKey = queryKeys.file('project-1', 'src/App.tsx');
    const fileFetch = vi.fn().mockResolvedValue({ path: 'src/App.tsx', content: 'server', sha: 'sha-1' });
    queryClient.setQueryData(queryKeys.files('project-1'), { root: '.', nodes: [] });
    queryClient.setQueryData(fileKey, { path: 'src/App.tsx', content: 'server', sha: 'sha-1' });
    const observer = new QueryObserver(queryClient, { queryKey: fileKey, queryFn: fileFetch, staleTime: Infinity });
    const unsubscribe = observer.subscribe(() => undefined);

    renderPanel(queryClient);
    const user = userEvent.setup();
    await user.type(await screen.findByPlaceholderText('Describe the change you want...'), 'Update files');
    await user.click(screen.getByRole('button', { name: 'Send' }));

    await waitFor(() => {
      expect(queryClient.getQueryState(queryKeys.files('project-1'))?.isInvalidated).toBe(true);
    });
    await waitFor(() => expect(fileFetch).toHaveBeenCalled());
    unsubscribe();
  });
});
