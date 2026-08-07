import { useEffect, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useParams } from '@tanstack/react-router';
import { AlertCircle, Box, Code2, Eye, LoaderCircle, MessageSquare } from 'lucide-react';
import { getProject, PROJECT_RUNTIME_EXPIRED_EVENT, type ProjectRuntimeExpiredDetail } from '../api';
import { useI18n } from '../i18n';
import { queryKeys } from '../queryKeys';
import { useWorkspaceStore, type WorkspaceTab } from '../stores/workspace';
import AppHeader from './AppHeader';
import ChatPanel from './ChatPanel';
import CodeEditor from './CodeEditor';
import PreviewPanel from './PreviewPanel';
import PublishPanel from './PublishPanel';

const TAB_ICONS: Record<WorkspaceTab, typeof MessageSquare> = {
  chat: MessageSquare,
  preview: Eye,
  code: Code2,
  publish: Box,
};

export default function Workspace() {
  const { projectId } = useParams({ from: '/projects/$projectId' });
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { mobileTab, rightTab, setMobileTab, setRightTab, reset } = useWorkspaceStore();
  const [desktopLayout, setDesktopLayout] = useState(() => window.matchMedia('(min-width: 1024px)').matches);
  const [expiredProjectId, setExpiredProjectId] = useState<string | null>(null);
  const handledRuntimeExpiryRef = useRef<string | null>(null);
  const projectQuery = useQuery({
    queryKey: queryKeys.project(projectId),
    queryFn: () => getProject(projectId),
    refetchInterval: (query) => query.state.data?.active_run_id ? 5_000 : 30_000,
    refetchIntervalInBackground: true,
  });

  useEffect(() => {
    reset();
    setExpiredProjectId(null);
    handledRuntimeExpiryRef.current = null;
  }, [projectId, reset]);

  useEffect(() => {
    const handleRuntimeExpired = (event: Event) => {
      const expiredId = (event as CustomEvent<ProjectRuntimeExpiredDetail>).detail?.projectId;
      if (expiredId !== projectId) return;
      if (handledRuntimeExpiryRef.current === projectId) return;
      handledRuntimeExpiryRef.current = projectId;
      setExpiredProjectId(projectId);
      void queryClient.refetchQueries({ queryKey: queryKeys.project(projectId), exact: true, type: 'active' });
    };
    window.addEventListener(PROJECT_RUNTIME_EXPIRED_EVENT, handleRuntimeExpired);
    return () => window.removeEventListener(PROJECT_RUNTIME_EXPIRED_EVENT, handleRuntimeExpired);
  }, [projectId, queryClient]);

  useEffect(() => {
    const media = window.matchMedia('(min-width: 1024px)');
    const update = () => setDesktopLayout(media.matches);
    update();
    media.addEventListener('change', update);
    return () => media.removeEventListener('change', update);
  }, []);

  if (projectQuery.isLoading) {
    return <div className="flex min-h-[100dvh] items-center justify-center bg-slate-50 text-sm text-slate-500"><LoaderCircle size={18} className="mr-2 animate-spin" />{t('common.loading')}</div>;
  }

  if (!projectQuery.data) {
    return (
      <div className="min-h-[100dvh] bg-slate-50">
        <AppHeader />
        <main className="mx-auto mt-12 flex max-w-lg items-start gap-3 rounded-md border border-red-200 bg-red-50 p-4 text-sm text-red-800"><AlertCircle size={18} className="shrink-0" />{projectQuery.error?.message}</main>
      </div>
    );
  }

  const project = projectQuery.data;
  const runtimeStatus = expiredProjectId === projectId ? 'expired' : project.runtime_status;
  const chatReadOnly = runtimeStatus === 'expired';
  const workspaceReadOnly = runtimeStatus !== 'active';
  const runtimeMessage = runtimeStatus === 'expired' ? t('workspace.runtimeExpired') : runtimeStatus === 'unavailable' ? t('workspace.runtimeUnavailable') : '';
  const tabLabels: Record<WorkspaceTab, string> = {
    chat: t('workspace.chat'),
    preview: t('workspace.preview'),
    code: t('workspace.code'),
    publish: t('workspace.publish'),
  };

  const panel = (tab: WorkspaceTab) => {
    if (tab === 'chat') return <ChatPanel key={`chat:${project.id}`} project={project} readOnly={chatReadOnly} />;
    if (tab === 'preview') return <PreviewPanel key={`preview:${project.id}`} projectId={project.id} readOnly={workspaceReadOnly} />;
    if (tab === 'code') return <CodeEditor key={`code:${project.id}`} projectId={project.id} readOnly={workspaceReadOnly} />;
    return <PublishPanel key={`publish:${project.id}`} projectId={project.id} readOnly={workspaceReadOnly || Boolean(project.active_run_id)} />;
  };

  return (
    <div className="flex h-[100dvh] min-h-0 flex-col overflow-hidden bg-slate-50 text-slate-950">
      <AppHeader projectName={project.name} />
      {runtimeMessage && (
        <div role="status" className="flex shrink-0 items-start gap-2 border-b border-amber-200 bg-amber-50 px-4 py-2 text-xs leading-5 text-amber-900">
          <AlertCircle size={15} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1">{runtimeMessage}</span>
          {runtimeStatus === 'expired' && <Link to="/projects" className="shrink-0 rounded-md px-2 font-medium underline underline-offset-2 hover:bg-amber-100">{t('workspace.backToProjects')}</Link>}
        </div>
      )}

      {desktopLayout ? (
        <main className="grid min-h-0 flex-1 grid-cols-[minmax(360px,42%)_minmax(0,58%)]">
          <div className="min-h-0 border-r border-slate-200">{panel('chat')}</div>
          <div className="flex min-h-0 min-w-0 flex-col">
            <div className="flex h-11 shrink-0 items-center border-b border-slate-200 bg-white px-2" role="tablist">
              {(['preview', 'code', 'publish'] as const).map((tab) => {
                const Icon = TAB_ICONS[tab];
                return (
                  <button key={tab} type="button" role="tab" aria-selected={rightTab === tab} onClick={() => setRightTab(tab)} className={`flex h-8 items-center gap-2 rounded-md px-3 text-sm font-medium ${rightTab === tab ? 'bg-slate-900 text-white' : 'text-slate-600 hover:bg-slate-100'}`}>
                    <Icon size={15} />{tabLabels[tab]}
                  </button>
                );
              })}
            </div>
            <div className="min-h-0 flex-1">{panel(rightTab)}</div>
          </div>
        </main>
      ) : (
        <main className="flex min-h-0 flex-1 flex-col">
          <div className="flex h-11 shrink-0 items-center border-b border-slate-200 bg-white px-2" role="tablist">
            {(['chat', 'preview', 'code', 'publish'] as const).map((tab) => {
              const Icon = TAB_ICONS[tab];
              return (
                <button key={tab} type="button" role="tab" aria-selected={mobileTab === tab} onClick={() => setMobileTab(tab)} className={`flex h-8 min-w-0 flex-1 items-center justify-center gap-1.5 rounded-md px-2 text-xs font-medium sm:text-sm ${mobileTab === tab ? 'bg-slate-900 text-white' : 'text-slate-600 hover:bg-slate-100'}`}>
                  <Icon size={14} className="shrink-0" /><span className="truncate">{tabLabels[tab]}</span>
                </button>
              );
            })}
          </div>
          <div className="min-h-0 flex-1">{panel(mobileTab)}</div>
        </main>
      )}
    </div>
  );
}
