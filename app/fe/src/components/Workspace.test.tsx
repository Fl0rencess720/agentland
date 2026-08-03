import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { describe, expect, it, vi } from 'vitest';
import { getFileTree, PROJECT_RUNTIME_EXPIRED_EVENT } from '../api';
import { I18nProvider } from '../i18n';
import { server } from '../test/server';
import Workspace from './Workspace';

vi.mock('@tanstack/react-router', () => ({
  Link: ({ children }: { children: React.ReactNode }) => <a href="/projects">{children}</a>,
  useParams: () => ({ projectId: 'project-1' }),
}));

vi.mock('./AppHeader', () => ({ default: () => <header>Agentland</header> }));
vi.mock('./ChatPanel', () => ({
  default: ({ readOnly }: { readOnly: boolean }) => <div data-testid="chat-mode">{readOnly ? 'read-only' : 'editable'}</div>,
}));
vi.mock('./CodeEditor', () => ({
  default: ({ readOnly }: { readOnly: boolean }) => <div data-testid="code-mode">{readOnly ? 'read-only' : 'editable'}</div>,
}));
vi.mock('./PreviewPanel', () => ({
  default: ({ readOnly }: { readOnly: boolean }) => <div data-testid="preview-mode">{readOnly ? 'read-only' : 'editable'}</div>,
}));

function envelope<T>(data: T, status = 200) {
  return HttpResponse.json({ msg: status === 200 ? 'ok' : 'runtime expired', code: status, data }, { status });
}

describe('Workspace', () => {
  it('enters read-only mode and refreshes the project after any runtime expiry response', async () => {
    localStorage.setItem('agentland.locale', 'en-US');
    let projectReads = 0;
    server.use(
      http.get('/api/v1/projects/project-1', () => {
        projectReads += 1;
        return envelope({ id: 'project-1', name: 'Demo', status: 'DRAFT', runtime_status: 'active' });
      }),
      http.get('/api/v1/projects/project-1/files/tree', () => envelope({ type: 'PROJECT_RUNTIME_EXPIRED' }, 410)),
    );
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <I18nProvider><Workspace /></I18nProvider>
      </QueryClientProvider>,
    );

    expect(await screen.findByTestId('chat-mode')).toHaveTextContent('editable');
    window.dispatchEvent(new CustomEvent(PROJECT_RUNTIME_EXPIRED_EVENT, { detail: { projectId: 'project-2' } }));
    expect(screen.queryByText(/This runtime has expired/)).not.toBeInTheDocument();
    expect(screen.getByTestId('chat-mode')).toHaveTextContent('editable');
    await act(async () => {
      await expect(getFileTree('project-1')).rejects.toMatchObject({ code: 'PROJECT_RUNTIME_EXPIRED' });
    });
    expect(await screen.findByText(/This runtime has expired/)).toBeInTheDocument();
    expect(screen.getByTestId('chat-mode')).toHaveTextContent('read-only');
    await waitFor(() => expect(projectReads).toBeGreaterThanOrEqual(2));

    const user = userEvent.setup();
    await user.click(screen.getByRole('tab', { name: 'Code' }));
    expect(screen.getByTestId('code-mode')).toHaveTextContent('read-only');
    await user.click(screen.getByRole('tab', { name: 'Preview' }));
    expect(screen.getByTestId('preview-mode')).toHaveTextContent('read-only');
  });
});
