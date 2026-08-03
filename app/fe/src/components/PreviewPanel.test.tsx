import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';
import { I18nProvider } from '../i18n';
import { server } from '../test/server';
import PreviewPanel from './PreviewPanel';

function renderPreview(previewURL: string) {
  localStorage.setItem('agentland.locale', 'en-US');
  server.use(http.get('/api/v1/projects/project-1/preview', () => HttpResponse.json({
    msg: 'ok',
    code: 200,
    data: { status: 'running', port: 3000, preview_url: previewURL },
  })));
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <I18nProvider><PreviewPanel projectId="project-1" readOnly={false} /></I18nProvider>
    </QueryClientProvider>,
  );
}

describe('PreviewPanel', () => {
  it('enables same-origin capabilities only for an isolated absolute preview URL', async () => {
    renderPreview('https://preview-token.preview.example.test/p/preview-token/');

    const frame = await screen.findByTitle('Application preview');
    expect(frame).toHaveAttribute('src', 'https://preview-token.preview.example.test/p/preview-token/');
    expect(frame).toHaveAttribute('sandbox', expect.stringContaining('allow-same-origin'));
  });

  it('rejects a preview URL on the Agentland origin', async () => {
    renderPreview(`${window.location.origin}/p/preview-token/`);

    expect(await screen.findByRole('alert')).toHaveTextContent('must use a valid origin isolated from Agentland');
    expect(screen.queryByTitle('Application preview')).not.toBeInTheDocument();
  });

  it('rejects a relative preview URL', async () => {
    renderPreview('/p/preview-token/');

    expect(await screen.findByRole('alert')).toHaveTextContent('must use a valid origin isolated from Agentland');
    expect(screen.queryByTitle('Application preview')).not.toBeInTheDocument();
  });
});
