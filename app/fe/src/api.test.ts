import { http, HttpResponse } from 'msw';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  completeGithubAuth,
  getFileTree,
  PROJECT_RUNTIME_EXPIRED_EVENT,
  startGithubAuth,
  streamRunEvents,
  type RunEvent,
} from './api';
import { useAuthStore } from './stores/auth';
import { server } from './test/server';

function event(type: RunEvent['type'], sequence: number) {
  return {
    type,
    run_id: 'run-1',
    conversation_id: 'conversation-1',
    sequence,
    timestamp: '2026-08-02T10:00:00Z',
    payload: type === 'message.delta' ? { delta: 'hello' } : {},
  } satisfies RunEvent;
}

describe('streamRunEvents', () => {
  afterEach(() => useAuthStore.getState().clear());

  it('sends authorization and Last-Event-ID headers and parses SSE frames', async () => {
    useAuthStore.getState().setSession({ accessToken: 'access-token', refreshToken: 'refresh-token', user: null });
    const seenHeaders: Record<string, string | null> = {};
    const encoder = new TextEncoder();

    server.use(http.get('/api/v1/runs/run-1/events', ({ request }) => {
      seenHeaders.authorization = request.headers.get('Authorization');
      seenHeaders.lastEventId = request.headers.get('Last-Event-ID');
      seenHeaders.credentials = request.credentials;
      const body = [
        `id: 124-0\nevent: message.delta\ndata: ${JSON.stringify(event('message.delta', 1))}\n\n`,
        `id: 125-0\nevent: run.completed\ndata: ${JSON.stringify(event('run.completed', 2))}\n\n`,
      ].join('');
      return new HttpResponse(new ReadableStream({
        start(controller) {
          controller.enqueue(encoder.encode(body));
          controller.close();
        },
      }), { headers: { 'Content-Type': 'text/event-stream' } });
    }));

    const received: RunEvent[] = [];
    const ids: string[] = [];
    await streamRunEvents('run-1', {
      signal: new AbortController().signal,
      lastEventId: '123-0',
      onEvent: (value) => received.push(value),
      onLastEventId: (id) => ids.push(id),
    });

    expect(seenHeaders).toEqual({ authorization: 'Bearer access-token', lastEventId: '123-0', credentials: 'include' });
    expect(received.map((value) => value.type)).toEqual(['message.delta', 'run.completed']);
    expect(ids).toEqual(['124-0', '125-0']);
  });

  it('includes credentials in GitHub OAuth start and callback requests', async () => {
    const credentials: RequestCredentials[] = [];
    server.use(
      http.post('/api/v1/auth/github/start', ({ request }) => {
        credentials.push(request.credentials);
        return HttpResponse.json({
          msg: 'success',
          code: 200,
          data: { authorize_url: 'https://github.com/login/oauth/authorize', state: 'state-1' },
        });
      }),
      http.post('/api/v1/auth/github/callback', ({ request }) => {
        credentials.push(request.credentials);
        return HttpResponse.json({
          msg: 'success',
          code: 200,
          data: {
            user: { id: 'user-1', email: 'user@example.com', name: 'User' },
            access_token: 'access-token',
            refresh_token: 'refresh-token',
            expires_in: 900,
          },
        });
      }),
    );

    await startGithubAuth('http://localhost:3000/login');
    await completeGithubAuth('github-code', 'state-1');

    expect(credentials).toEqual(['include', 'include']);
  });

  it('includes credentials in API requests and token refresh retries', async () => {
    useAuthStore.getState().setSession({ accessToken: 'expired-token', refreshToken: 'refresh-token', user: null });
    const credentials: RequestCredentials[] = [];
    let treeRequests = 0;
    server.use(
      http.get('/api/v1/projects/project-1/files/tree', ({ request }) => {
        credentials.push(request.credentials);
        treeRequests += 1;
        if (treeRequests === 1) {
          return HttpResponse.json({ msg: 'unauthorized', code: 401, data: { type: 'UNAUTHORIZED' } }, { status: 401 });
        }
        return HttpResponse.json({ msg: 'success', code: 200, data: { root: '.', nodes: [] } });
      }),
      http.post('/api/v1/auth/refresh', ({ request }) => {
        credentials.push(request.credentials);
        return HttpResponse.json({
          msg: 'success',
          code: 200,
          data: { access_token: 'new-access-token', refresh_token: 'new-refresh-token', expires_in: 900 },
        });
      }),
    );

    await getFileTree('project-1');

    expect(credentials).toEqual(['include', 'include', 'include']);
    expect(useAuthStore.getState()).toMatchObject({ accessToken: 'new-access-token', refreshToken: 'new-refresh-token' });
  });

  it('reports runtime expiry from API errors', async () => {
    const listener = vi.fn();
    window.addEventListener(PROJECT_RUNTIME_EXPIRED_EVENT, listener);
    server.use(http.get('/api/v1/projects/project-1/files/tree', () => HttpResponse.json({
      msg: 'runtime expired',
      code: 410,
      data: { type: 'PROJECT_RUNTIME_EXPIRED' },
    }, { status: 410 })));

    await expect(getFileTree('project-1')).rejects.toMatchObject({
      status: 410,
      code: 'PROJECT_RUNTIME_EXPIRED',
    });
    expect(listener).toHaveBeenCalledOnce();
    expect(listener.mock.calls[0][0]).toMatchObject({ detail: { projectId: 'project-1' } });
    window.removeEventListener(PROJECT_RUNTIME_EXPIRED_EVENT, listener);
  });
});
