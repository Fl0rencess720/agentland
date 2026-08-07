import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertCircle, LoaderCircle, Monitor, Play, RefreshCw, Smartphone, Tablet } from 'lucide-react';
import { getPreview, startPreview } from '../api';
import { useI18n } from '../i18n';
import { queryKeys } from '../queryKeys';

type PreviewPanelProps = {
  projectId: string;
  readOnly: boolean;
};

function isolatedPreviewURL(raw: string | undefined) {
  if (!raw || typeof window === 'undefined') return null;
  try {
    const parsed = new URL(raw);
    if ((parsed.protocol !== 'http:' && parsed.protocol !== 'https:') || parsed.origin === window.location.origin) return null;
    return parsed.href;
  } catch {
    return null;
  }
}

export default function PreviewPanel({ projectId, readOnly }: PreviewPanelProps) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [port, setPort] = useState(3000);
  const [frameKey, setFrameKey] = useState(0);
  const [viewport, setViewport] = useState<'desktop' | 'tablet' | 'mobile'>('desktop');
  const previewQuery = useQuery({
    queryKey: queryKeys.preview(projectId),
    queryFn: () => getPreview(projectId),
    retry: false,
    enabled: !readOnly,
    refetchInterval: (query) => query.state.data?.status === 'starting' ? 1500 : false,
  });
  const startMutation = useMutation({
    mutationFn: () => startPreview(projectId, port),
    onSuccess: (preview) => queryClient.setQueryData(queryKeys.preview(projectId), preview),
  });
  const preview = previewQuery.data;
  const previewURL = isolatedPreviewURL(preview?.preview_url);
  const invalidPreviewURL = preview?.status === 'running' && !previewURL;
  const isStarting = startMutation.isPending || preview?.status === 'starting';
  const viewports = [
    ['desktop', Monitor, t('preview.desktop')],
    ['tablet', Tablet, t('preview.tablet')],
    ['mobile', Smartphone, t('preview.mobile')],
  ] as const;

  if (readOnly) {
    return (
      <section aria-label={t('workspace.preview')} className="flex h-full min-h-64 items-center justify-center bg-slate-100 p-6 text-center text-sm text-slate-500">
        <div><Monitor size={25} className="mx-auto mb-3 text-slate-400" />{t('preview.runtimeUnavailable')}</div>
      </section>
    );
  }

  return (
    <section aria-label={t('workspace.preview')} className="flex h-full min-h-0 flex-col bg-slate-100">
      <div className="flex min-h-11 shrink-0 flex-wrap items-center gap-1.5 border-b border-slate-200 bg-white px-2 py-1.5 sm:gap-2 sm:px-3">
        <label className="flex items-center gap-2 text-xs font-medium text-slate-600">
          {t('preview.port')}
          <input type="number" min={1} max={65535} value={port} onChange={(event) => setPort(Number(event.target.value))} disabled={readOnly || isStarting} className="h-8 w-16 rounded-md border border-slate-300 px-2 text-sm text-slate-900 outline-none focus:border-slate-500 sm:w-20" />
        </label>
        <button type="button" onClick={() => startMutation.mutate()} disabled={readOnly || isStarting} className="flex h-8 items-center gap-1.5 rounded-md bg-slate-900 px-2 text-xs font-medium text-white hover:bg-slate-800 disabled:bg-slate-300 sm:gap-2 sm:px-3">
          {isStarting ? <LoaderCircle size={13} className="animate-spin" /> : <Play size={13} />}{t('preview.start')}
        </button>
        <div className="flex h-8 items-center rounded-md border border-slate-200 p-0.5" role="group" aria-label={t('preview.viewport')}>
          {viewports.map(([value, Icon, label]) => (
            <button key={value} type="button" title={label} aria-label={label} aria-pressed={viewport === value} onClick={() => setViewport(value)} className={`flex h-7 w-7 items-center justify-center rounded-md sm:w-8 ${viewport === value ? 'bg-slate-900 text-white' : 'text-slate-500 hover:bg-slate-100'}`}><Icon size={14} /></button>
          ))}
        </div>
        <div className="ml-auto flex items-center gap-1">
          <button type="button" onClick={() => { void previewQuery.refetch(); setFrameKey((value) => value + 1); }} title={t('preview.refresh')} className="flex h-8 w-8 items-center justify-center rounded-md text-slate-600 hover:bg-slate-100"><RefreshCw size={15} /></button>
        </div>
      </div>

      <div className="min-h-0 flex-1 p-2 sm:p-3">
        {previewQuery.isLoading && <div className="flex h-full items-center justify-center text-sm text-slate-500"><LoaderCircle size={17} className="mr-2 animate-spin" />{t('common.loading')}</div>}
        {(previewQuery.isError || startMutation.isError || preview?.status === 'failed') && (
          <div role="alert" className="mx-auto mt-8 flex max-w-lg items-start gap-2 rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-800"><AlertCircle size={16} className="mt-0.5 shrink-0" />{preview?.error || startMutation.error?.message || (previewQuery.error as Error)?.message || t('preview.failed')}</div>
        )}
        {isStarting && <div role="status" aria-live="polite" className="flex h-full items-center justify-center text-sm text-slate-500"><LoaderCircle size={17} className="mr-2 animate-spin" />{t('preview.starting')}</div>}
        {!previewQuery.isLoading && !isStarting && invalidPreviewURL && (
          <div role="alert" className="mx-auto mt-8 flex max-w-lg items-start gap-2 rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-800"><AlertCircle size={16} className="mt-0.5 shrink-0" />{t('preview.invalidOrigin')}</div>
        )}
        {!previewQuery.isLoading && !isStarting && preview?.status === 'running' && previewURL && (
          <div className="h-full overflow-auto text-center">
            <iframe
              key={frameKey}
              src={previewURL}
              title="Application preview"
              sandbox="allow-forms allow-modals allow-popups allow-same-origin allow-scripts"
              referrerPolicy="no-referrer"
              className={`inline-block h-full max-w-none rounded-md border border-slate-300 bg-white text-left shadow-sm ${viewport === 'desktop' ? 'w-full' : viewport === 'tablet' ? 'w-[768px]' : 'w-[390px]'}`}
            />
          </div>
        )}
        {!previewQuery.isLoading && !isStarting && (!preview || preview.status === 'idle' || preview.status === 'expired') && (
          <div className="flex h-full min-h-64 items-center justify-center border border-dashed border-slate-300 bg-white p-8 text-center text-sm leading-6 text-slate-500">{t('preview.empty')}</div>
        )}
      </div>
    </section>
  );
}
