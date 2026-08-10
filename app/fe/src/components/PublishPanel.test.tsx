import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';
import { I18nProvider } from '../i18n';
import { server } from '../test/server';
import PublishPanel from './PublishPanel';

function envelope<T>(data: T, status = 200) {
  return HttpResponse.json({ msg: status === 202 ? 'accepted' : 'ok', code: status, data }, { status });
}

function renderPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <I18nProvider><PublishPanel projectId="project-1" readOnly={false} /></I18nProvider>
    </QueryClientProvider>,
  );
}

describe('PublishPanel', () => {
  it('creates an idempotent publication and shows preparing status', async () => {
    localStorage.setItem('agentland.locale', 'en-US');
    let idempotency = '';
    let requestBody: Record<string, string> = {};
    server.use(
      http.get('/api/v1/projects/project-1/publications', () => envelope({ items: [] })),
      http.post('/api/v1/projects/project-1/publications', async ({ request }) => {
        idempotency = request.headers.get('Idempotency-Key') ?? '';
        requestBody = await request.json() as Record<string, string>;
        return envelope({
          id: 'pub-1', project_id: 'project-1', status: 'preparing', preparation_run_id: 'run-prepare-1', context: requestBody.context,
          dockerfile: requestBody.dockerfile, created_at: '2026-01-01T00:00:00Z',
        }, 202);
      }),
    );
    renderPanel();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Deploy' }));
    await waitFor(() => expect(screen.getAllByText('Preparing Dockerfile').length).toBeGreaterThan(0));
    expect(idempotency).not.toBe('');
    expect(requestBody).toEqual({ context: '.', dockerfile: 'Dockerfile' });
  });

  it('shows the immutable digest for a completed publication', async () => {
    localStorage.setItem('agentland.locale', 'en-US');
    server.use(http.get('/api/v1/projects/project-1/publications', () => envelope({ items: [{
      id: 'pub-1', project_id: 'project-1', status: 'completed', context: '.', dockerfile: 'Dockerfile',
      image_ref: 'registry.example/apps/project-1:pub-1', digest: `sha256:${'a'.repeat(64)}`,
      deployment_url: 'https://app-123.apps.example.com', deployment_hostname: 'app-123.apps.example.com',
      logs: 'build complete', created_at: '2026-01-01T00:00:00Z',
    }] })));
    renderPanel();
    expect(await screen.findByText(/registry\.example\/apps\/project-1@sha256:/)).toBeInTheDocument();
    expect(screen.getByText('build complete')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /app-123\.apps\.example\.com/ })).toHaveAttribute('href', 'https://app-123.apps.example.com');
  });
});
