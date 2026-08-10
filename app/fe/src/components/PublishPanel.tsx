import { useMemo, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertCircle, Ban, Box, Check, Clipboard, ExternalLink, LoaderCircle, Rocket, Terminal } from 'lucide-react';
import { cancelPublication, createPublication, listPublications, type Publication } from '../api';
import { useI18n } from '../i18n';
import { queryKeys } from '../queryKeys';

type PublishPanelProps = {
  projectId: string;
  readOnly: boolean;
};

function immutableImage(item: Publication) {
  if (!item.image_ref || !item.digest) return '';
  return `${item.image_ref.split(':').slice(0, -1).join(':')}@${item.digest}`;
}

function idempotencyKey() {
  return typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `publication-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

export default function PublishPanel({ projectId, readOnly }: PublishPanelProps) {
  const { locale, t } = useI18n();
  const queryClient = useQueryClient();
  const [buildContext, setBuildContext] = useState('.');
  const [dockerfile, setDockerfile] = useState('Dockerfile');
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const pendingRequest = useRef<{ signature: string; key: string } | null>(null);
  const publicationsQuery = useQuery({
    queryKey: queryKeys.publications(projectId),
    queryFn: () => listPublications(projectId),
    refetchInterval: (query) => query.state.data?.items.some((item) => item.status === 'preparing' || item.status === 'queued' || item.status === 'running') ? 1_500 : false,
    refetchIntervalInBackground: true,
  });
  const createMutation = useMutation({
    mutationFn: () => {
      const context = buildContext.trim();
      const file = dockerfile.trim();
      const signature = `${context}\n${file}`;
      if (pendingRequest.current?.signature !== signature) pendingRequest.current = { signature, key: idempotencyKey() };
      return createPublication(projectId, context, file, pendingRequest.current.key);
    },
    onSuccess: (publication) => {
      pendingRequest.current = null;
      setSelectedId(publication.id);
      queryClient.setQueryData(queryKeys.publications(projectId), (current: { items: Publication[] } | undefined) => ({
        items: [publication, ...(current?.items.filter((item) => item.id !== publication.id) ?? [])],
      }));
    },
  });
  const cancelMutation = useMutation({
    mutationFn: (publicationId: string) => cancelPublication(publicationId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.publications(projectId) }),
  });
  const items = publicationsQuery.data?.items ?? [];
  const selected = useMemo(
    () => items.find((item) => item.id === selectedId) ?? items[0],
    [items, selectedId],
  );
  const active = items.find((item) => item.status === 'preparing' || item.status === 'queued' || item.status === 'running');
  const image = selected ? immutableImage(selected) : '';
  const canPublish = !readOnly && !active && buildContext.trim() !== '' && dockerfile.trim() !== '';
  const displayError = createMutation.error?.message || publicationsQuery.error?.message;

  const copyImage = async () => {
    if (!image) return;
    await navigator.clipboard.writeText(image);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1_500);
  };

  return (
    <section aria-label={t('workspace.publish')} className="flex h-full min-h-0 flex-col overflow-hidden bg-white">
      <div className="shrink-0 border-b border-slate-200 px-3 py-3 sm:px-4">
        <div className="mb-3 flex items-center gap-2 text-sm font-semibold text-slate-900"><Box size={16} />{t('publish.title')}</div>
        <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]">
          <label className="min-w-0 text-xs font-medium text-slate-600">
            {t('publish.context')}
            <input value={buildContext} onChange={(event) => setBuildContext(event.target.value)} disabled={readOnly || Boolean(active)} className="mt-1 h-9 w-full rounded-md border border-slate-300 px-2.5 font-mono text-sm text-slate-900 disabled:bg-slate-100" />
          </label>
          <label className="min-w-0 text-xs font-medium text-slate-600">
            {t('publish.dockerfile')}
            <input value={dockerfile} onChange={(event) => setDockerfile(event.target.value)} disabled={readOnly || Boolean(active)} className="mt-1 h-9 w-full rounded-md border border-slate-300 px-2.5 font-mono text-sm text-slate-900 disabled:bg-slate-100" />
          </label>
          <button type="button" onClick={() => createMutation.mutate()} disabled={!canPublish || createMutation.isPending} className="mt-auto flex h-9 items-center justify-center gap-2 rounded-md bg-slate-900 px-3 text-sm font-medium text-white hover:bg-slate-800 disabled:bg-slate-300">
            {createMutation.isPending ? <LoaderCircle size={15} className="animate-spin" /> : <Rocket size={15} />}{t('publish.start')}
          </button>
        </div>
        {readOnly && <p className="mt-2 text-xs leading-5 text-amber-700">{t('publish.runtimeUnavailable')}</p>}
        {displayError && <div role="alert" className="mt-2 flex items-start gap-2 text-xs leading-5 text-red-700"><AlertCircle size={14} className="mt-0.5 shrink-0" />{displayError}</div>}
      </div>

      <div className="grid min-h-0 flex-1 grid-rows-[minmax(120px,35%)_minmax(0,65%)] lg:grid-cols-[minmax(220px,32%)_minmax(0,68%)] lg:grid-rows-1">
        <aside className="min-h-0 overflow-auto border-b border-slate-200 lg:border-b-0 lg:border-r" aria-label={t('publish.history')}>
          <h2 className="sticky top-0 z-10 border-b border-slate-200 bg-slate-50 px-3 py-2 text-xs font-semibold uppercase text-slate-500">{t('publish.history')}</h2>
          {publicationsQuery.isLoading && <div className="flex items-center justify-center p-6 text-sm text-slate-500"><LoaderCircle size={16} className="mr-2 animate-spin" />{t('common.loading')}</div>}
          {!publicationsQuery.isLoading && items.length === 0 && <p className="p-5 text-center text-sm text-slate-500">{t('publish.empty')}</p>}
          {items.map((item) => (
            <button key={item.id} type="button" onClick={() => setSelectedId(item.id)} className={`block w-full border-b border-slate-100 px-3 py-3 text-left ${selected?.id === item.id ? 'bg-blue-50' : 'hover:bg-slate-50'}`}>
              <span className="flex items-center justify-between gap-2">
                <span className="truncate font-mono text-xs text-slate-700">{item.id}</span>
                <StatusIcon item={item} />
              </span>
              <span className="mt-1 block text-xs text-slate-500">{t(`publish.${item.status}`)} · {new Date(item.created_at).toLocaleString(locale)}</span>
            </button>
          ))}
        </aside>

        <div className="min-h-0 overflow-auto bg-slate-50 p-3 sm:p-4">
          {!selected && <div className="flex h-full min-h-52 items-center justify-center text-sm text-slate-500">{t('publish.empty')}</div>}
          {selected && (
            <div className="mx-auto max-w-4xl space-y-4">
              <div className="flex flex-wrap items-center gap-2">
                <StatusIcon item={selected} />
                <strong className="text-sm text-slate-900">{t(`publish.${selected.status}`)}</strong>
                {(selected.status === 'preparing' || selected.status === 'queued' || selected.status === 'running') && (
                  <button type="button" onClick={() => cancelMutation.mutate(selected.id)} disabled={cancelMutation.isPending} className="ml-auto flex h-8 items-center gap-1.5 rounded-md border border-slate-300 bg-white px-2.5 text-xs font-medium text-slate-700 hover:bg-slate-100 disabled:text-slate-400"><Ban size={14} />{t('common.cancel')}</button>
                )}
              </div>
              {image && (
                <section className="border-y border-slate-200 bg-white py-3">
                  <h3 className="mb-2 text-xs font-semibold uppercase text-slate-500">{t('publish.image')}</h3>
                  <div className="flex items-start gap-2">
                    <code className="min-w-0 flex-1 break-all text-xs leading-5 text-slate-800">{image}</code>
                    <button type="button" onClick={() => void copyImage()} title={t('publish.image')} className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-slate-600 hover:bg-slate-100">{copied ? <Check size={15} /> : <Clipboard size={15} />}</button>
                  </div>
                </section>
              )}
              {selected.deployment_url && (
                <section className="border-y border-slate-200 bg-white py-3">
                  <h3 className="mb-2 text-xs font-semibold uppercase text-slate-500">{t('publish.application')}</h3>
                  <a href={selected.deployment_url} target="_blank" rel="noreferrer" className="inline-flex min-w-0 items-center gap-2 text-sm font-medium text-blue-700 hover:text-blue-800 hover:underline">
                    <span className="break-all">{selected.deployment_hostname || selected.deployment_url}</span><ExternalLink size={14} className="shrink-0" />
                  </a>
                </section>
              )}
              {(selected.error_message || selected.error_code) && <div role="alert" className="flex items-start gap-2 border border-red-200 bg-red-50 p-3 text-sm text-red-800"><AlertCircle size={16} className="mt-0.5 shrink-0" /><span className="break-words"><strong>{selected.error_code}</strong>{selected.error_message && `: ${selected.error_message}`}</span></div>}
              {(selected.logs || selected.status === 'preparing' || selected.status === 'running' || selected.status === 'queued') && (
                <section>
                  <h3 className="mb-2 flex items-center gap-2 text-xs font-semibold uppercase text-slate-500"><Terminal size={14} />{t('publish.logs')}</h3>
                  <pre className="chat-scrollbar min-h-36 max-h-[55vh] overflow-auto rounded-md bg-zinc-950 p-3 font-mono text-xs leading-5 text-zinc-200">{selected.logs || t(`publish.${selected.status}`)}</pre>
                </section>
              )}
            </div>
          )}
        </div>
      </div>
    </section>
  );
}

function StatusIcon({ item }: { item: Publication }) {
  if (item.status === 'preparing' || item.status === 'queued' || item.status === 'running') return <LoaderCircle aria-hidden size={15} className="shrink-0 animate-spin text-blue-600" />;
  if (item.status === 'completed') return <Check aria-hidden size={15} className="shrink-0 text-emerald-600" />;
  if (item.status === 'cancelled') return <Ban aria-hidden size={15} className="shrink-0 text-slate-500" />;
  return <AlertCircle aria-hidden size={15} className="shrink-0 text-red-600" />;
}
