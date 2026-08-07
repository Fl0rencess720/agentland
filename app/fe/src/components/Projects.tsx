import { useDeferredValue, useEffect, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { AlertCircle, Folder, LoaderCircle, Plus, Search, Trash2 } from 'lucide-react';
import { createProject, deleteProject, listProjects } from '../api';
import { useI18n } from '../i18n';
import { queryKeys } from '../queryKeys';
import AppHeader from './AppHeader';

export default function Projects() {
  const { locale, t } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [search, setSearch] = useState('');
  const deferredSearch = useDeferredValue(search.trim());
  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState('');
  const createButtonRef = useRef<HTMLButtonElement>(null);
  const dialogRef = useRef<HTMLFormElement>(null);
  const nameInputRef = useRef<HTMLInputElement>(null);

  const projectsQuery = useQuery({
    queryKey: queryKeys.projects(deferredSearch),
    queryFn: () => listProjects(deferredSearch),
  });

  const createMutation = useMutation({
    mutationFn: () => createProject(name.trim() || t('projects.namePlaceholder')),
    onSuccess: async (project) => {
      await queryClient.invalidateQueries({ queryKey: ['projects'] });
      setCreateOpen(false);
      setName('');
      await navigate({ to: '/projects/$projectId', params: { projectId: project.id } });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: deleteProject,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['projects'] }),
  });

  useEffect(() => {
    if (!createOpen) return;
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : createButtonRef.current;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    nameInputRef.current?.focus();

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        setCreateOpen(false);
        return;
      }
      if (event.key !== 'Tab' || !dialogRef.current) return;
      const focusable = Array.from(dialogRef.current.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), [href], [tabindex]:not([tabindex="-1"])'));
      const first = focusable[0];
      const last = focusable.at(-1);
      if (!first || !last) return;
      if (event.shiftKey && (document.activeElement === first || !dialogRef.current.contains(document.activeElement))) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && (document.activeElement === last || !dialogRef.current.contains(document.activeElement))) {
        event.preventDefault();
        first.focus();
      }
    };

    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
      document.body.style.overflow = previousOverflow;
      previousFocus?.focus();
    };
  }, [createOpen]);

  const removeProject = (projectId: string) => {
    if (window.confirm(t('projects.deleteConfirm'))) deleteMutation.mutate(projectId);
  };

  const dateFormatter = new Intl.DateTimeFormat(locale, { dateStyle: 'medium' });

  return (
    <div className="min-h-[100dvh] bg-slate-50 text-slate-950">
      <AppHeader />
      <main className="mx-auto w-full max-w-7xl px-4 py-8 sm:px-6 lg:px-8">
        <div className="flex flex-col gap-5 border-b border-slate-200 pb-6 sm:flex-row sm:items-end sm:justify-between">
          <div>
            <h1 className="text-2xl font-semibold tracking-normal">{t('projects.title')}</h1>
            <p className="mt-1 text-sm text-slate-600">{t('projects.subtitle')}</p>
          </div>
          <button
            ref={createButtonRef}
            type="button"
            onClick={() => {
              createMutation.reset();
              setCreateOpen(true);
            }}
            className="flex h-10 items-center justify-center gap-2 rounded-md bg-slate-900 px-4 text-sm font-medium text-white hover:bg-slate-800"
          >
            <Plus size={16} />{t('projects.new')}
          </button>
        </div>

        <label className="mt-6 flex h-10 max-w-md items-center gap-2 rounded-md border border-slate-300 bg-white px-3 focus-within:border-slate-500">
          <Search size={16} className="text-slate-400" />
          <span className="sr-only">{t('projects.search')}</span>
          <input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={t('projects.search')}
            className="min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-slate-500"
          />
        </label>

        {projectsQuery.isLoading && (
          <div className="flex min-h-64 items-center justify-center text-sm text-slate-500"><LoaderCircle size={18} className="mr-2 animate-spin" />{t('common.loading')}</div>
        )}
        {projectsQuery.isError && (
          <div role="alert" className="mt-6 flex items-center justify-between gap-3 rounded-md border border-red-200 bg-red-50 p-4 text-sm text-red-800">
            <span className="flex items-center gap-2"><AlertCircle size={17} />{projectsQuery.error.message}</span>
            <button type="button" onClick={() => void projectsQuery.refetch()} className="rounded-md border border-red-300 px-3 py-1.5 font-medium hover:bg-red-100">{t('common.retry')}</button>
          </div>
        )}

        {projectsQuery.data && projectsQuery.data.items.length === 0 && (
          <div className="mt-8 flex min-h-64 flex-col items-center justify-center border-y border-dashed border-slate-300 text-center">
            <Folder size={30} className="text-slate-400" />
            <p className="mt-3 text-sm font-medium text-slate-700">{deferredSearch ? t('projects.emptySearch') : t('projects.empty')}</p>
          </div>
        )}

        {projectsQuery.data && projectsQuery.data.items.length > 0 && (
          <div className="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {projectsQuery.data.items.map((project) => (
              <article key={project.id} className="group relative rounded-md border border-slate-200 bg-white p-4 shadow-sm transition hover:border-slate-300 hover:shadow-md">
                <Link to="/projects/$projectId" params={{ projectId: project.id }} aria-label={`${t('projects.open')}: ${project.name}`} className="absolute inset-0 rounded-md" />
                <div className="relative flex items-start justify-between gap-3 pointer-events-none">
                  <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-md bg-slate-100 text-slate-600"><Folder size={19} /></span>
                  <button
                    type="button"
                    aria-label={`${t('common.delete')}: ${project.name}`}
                    onClick={() => removeProject(project.id)}
                    className="pointer-events-auto relative z-10 flex h-8 w-8 items-center justify-center rounded-md text-slate-400 opacity-100 hover:bg-red-50 hover:text-red-700 sm:opacity-0 sm:group-hover:opacity-100"
                  >
                    <Trash2 size={16} />
                  </button>
                </div>
                <h2 className="relative mt-4 truncate text-sm font-semibold text-slate-900 pointer-events-none">{project.name}</h2>
                <div className="relative mt-3 flex items-center justify-end pointer-events-none">
                  <span className="truncate text-xs text-slate-500">
                    {project.updated_at ? t('projects.updated', { date: dateFormatter.format(new Date(project.updated_at)) }) : project.status}
                  </span>
                </div>
              </article>
            ))}
          </div>
        )}
      </main>

      {createOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-slate-950/30 p-4" role="presentation" onMouseDown={() => setCreateOpen(false)}>
          <form
            ref={dialogRef}
            role="dialog"
            aria-modal="true"
            aria-labelledby="create-project-title"
            onSubmit={(event) => {
              event.preventDefault();
              createMutation.mutate();
            }}
            onMouseDown={(event) => event.stopPropagation()}
            className="w-full max-w-sm rounded-md border border-slate-200 bg-white p-5 shadow-xl"
          >
            <h2 id="create-project-title" className="text-base font-semibold">{t('projects.new')}</h2>
            <label className="mt-5 block text-sm font-medium text-slate-700">
              {t('projects.name')}
              <input ref={nameInputRef} value={name} onChange={(event) => setName(event.target.value)} placeholder={t('projects.namePlaceholder')} className="mt-2 h-10 w-full rounded-md border border-slate-300 px-3 text-sm outline-none focus:border-slate-500" />
            </label>
            {createMutation.isError && <p role="alert" className="mt-3 text-sm text-red-700">{createMutation.error.message}</p>}
            <div className="mt-6 flex justify-end gap-2">
              <button type="button" onClick={() => setCreateOpen(false)} className="h-9 rounded-md border border-slate-300 px-3 text-sm font-medium hover:bg-slate-50">{t('common.cancel')}</button>
              <button type="submit" disabled={createMutation.isPending} className="flex h-9 items-center gap-2 rounded-md bg-slate-900 px-3 text-sm font-medium text-white hover:bg-slate-800 disabled:opacity-60">
                {createMutation.isPending && <LoaderCircle size={15} className="animate-spin" />}{t('common.create')}
              </button>
            </div>
          </form>
        </div>
      )}
    </div>
  );
}
