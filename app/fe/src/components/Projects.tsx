import { useCallback, useEffect, useMemo, useState } from 'react';
import { motion } from 'motion/react';
import {
  LayoutGrid,
  Clock,
  Users,
  Search,
  Filter,
  ArrowUpDown,
  Plus,
  Trash2,
  MessageSquare,
  ShoppingCart,
  LayoutTemplate,
  BarChart2,
  ArrowLeft,
  Folder,
  HelpCircle,
  User,
  Loader2,
  AlertCircle,
} from 'lucide-react';
import { useI18n } from '../i18n';
import LanguageSwitcher from './LanguageSwitcher';
import {
  createProject,
  deleteProject,
  getProject,
  getProjectUsage,
  listProjects,
  type Project,
} from '../api';

type ProjectsProps = {
  onOpenEditor: (project: { id: string; name: string; viewMode?: 'preview' | 'code' }) => void;
  onBack: () => void;
  onProjects: () => void;
  onLogout: () => void;
  accessToken?: string;
};

type ViewMode = 'all' | 'recent' | 'shared';
type StatusFilter = 'all' | 'deployed' | 'draft' | 'building';

const STATUS_FILTERS: StatusFilter[] = ['all', 'deployed', 'draft', 'building'];

function fallbackIcon(index: number) {
  const icons = [
    <LayoutGrid size={48} className="text-blue-500" />,
    <ShoppingCart size={48} className="text-orange-500" />,
    <MessageSquare size={48} className="text-blue-400" />,
    <LayoutTemplate size={48} className="text-emerald-500" />,
    <BarChart2 size={48} className="text-indigo-500" />,
  ];
  return icons[index % icons.length];
}

function statusStyle(status: string) {
  switch (status.toUpperCase()) {
    case 'DEPLOYED':
    case 'SUCCESS':
      return 'text-green-400 bg-green-400/10 border border-green-400/20';
    case 'BUILDING':
    case 'RUNNING':
    case 'QUEUED':
      return 'text-orange-400 bg-orange-400/10 border border-orange-400/20';
    case 'DRAFT':
    default:
      return 'text-slate-400 bg-slate-400/10 border border-slate-400/20';
  }
}

function formatDate(iso?: string) {
  if (!iso) return '--';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '--';
  return date.toLocaleDateString();
}

export default function Projects({ onOpenEditor, onBack, onProjects, onLogout, accessToken }: ProjectsProps) {
  const { t } = useI18n();

  const [view, setView] = useState<ViewMode>('all');
  const [keyword, setKeyword] = useState('');
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all');
  const [sortOrder, setSortOrder] = useState<'asc' | 'desc'>('desc');

  const [projects, setProjects] = useState<Project[]>([]);
  const [usage, setUsage] = useState({ used: 0, limit: 0 });

  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [openingProjectId, setOpeningProjectId] = useState<string | null>(null);
  const [deletingProjectId, setDeletingProjectId] = useState<string | null>(null);
  const [creatingProject, setCreatingProject] = useState(false);

  const loadProjects = useCallback(async () => {
    setLoading(true);
    setError(null);

    try {
      const [projectData, usageData] = await Promise.all([
        listProjects(
          {
            view,
            keyword: keyword.trim() || undefined,
            status: statusFilter === 'all' ? undefined : statusFilter,
            sort_by: 'updated_at',
            sort_order: sortOrder,
            page: 1,
            page_size: 20,
          },
          accessToken,
        ),
        getProjectUsage(accessToken),
      ]);

      setProjects(projectData.items ?? []);
      setUsage({
        used: usageData.used ?? 0,
        limit: usageData.limit ?? 0,
      });
    } catch (loadError) {
      setError((loadError as Error).message || 'Failed to load projects.');
    } finally {
      setLoading(false);
    }
  }, [accessToken, keyword, sortOrder, statusFilter, view]);

  useEffect(() => {
    const timer = setTimeout(() => {
      void loadProjects();
    }, 250);
    return () => clearTimeout(timer);
  }, [loadProjects]);

  const openProject = async (projectId: string) => {
    setOpeningProjectId(projectId);
    setError(null);
    try {
      const project = await getProject(projectId, accessToken);
      onOpenEditor({
        id: project.id,
        name: project.name,
        viewMode: project.metadata?.last_view_mode ?? 'preview',
      });
    } catch (openError) {
      setError((openError as Error).message || 'Failed to open project.');
    } finally {
      setOpeningProjectId(null);
    }
  };

  const removeProject = async (projectId: string) => {
    setDeletingProjectId(projectId);
    setError(null);

    try {
      await deleteProject(projectId, accessToken);
      await loadProjects();
    } catch (deleteError) {
      setError((deleteError as Error).message || 'Failed to delete project.');
    } finally {
      setDeletingProjectId(null);
    }
  };

  const createAndOpenProject = async () => {
    setCreatingProject(true);
    setError(null);
    try {
      const project = await createProject({ name: 'Untitled Project', template: 'blank' }, accessToken);
      onOpenEditor({ id: project.id, name: project.name, viewMode: 'preview' });
    } catch (createError) {
      setError((createError as Error).message || 'Failed to create project.');
    } finally {
      setCreatingProject(false);
    }
  };

  const usageText = useMemo(() => {
    return t('projects.usageCount', { used: usage.used, limit: usage.limit });
  }, [t, usage.limit, usage.used]);

  const progress = usage.limit > 0 ? Math.min(100, Math.round((usage.used / usage.limit) * 100)) : 0;

  return (
    <motion.div
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      transition={{ duration: 0.3 }}
      className="min-h-screen flex flex-col bg-[#0B1120] text-white font-sans"
    >
      <header className="flex items-center justify-between px-6 py-3 border-b border-slate-800/50 bg-[#0B1120] shrink-0">
        <div className="flex items-center gap-3">
          <button onClick={onBack} className="p-2 -ml-2 text-slate-400 hover:text-white transition-colors rounded-lg hover:bg-slate-800/50">
            <ArrowLeft size={20} />
          </button>
          <div className="w-6 h-6 text-blue-500">
            <svg viewBox="0 0 24 24" fill="currentColor">
              <path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5" />
            </svg>
          </div>
          <span className="text-lg font-bold tracking-tight">AI App Gen</span>
        </div>
        <div className="flex items-center gap-6">
          <div className="flex items-center gap-4 border-r border-slate-800 pr-6">
            <button
              onClick={createAndOpenProject}
              disabled={creatingProject}
              className="bg-blue-600 hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed text-white px-4 py-1.5 rounded-lg text-sm font-medium flex items-center gap-2 transition-colors shadow-lg shadow-blue-500/20"
            >
              {creatingProject ? <Loader2 size={16} className="animate-spin" /> : <Plus size={16} />} {t('projects.newApp')}
            </button>
          </div>
          <div className="flex items-center gap-6">
            <LanguageSwitcher />
            <button onClick={onProjects} className="flex items-center gap-2 text-sm text-slate-400 hover:text-slate-200 transition-colors">
              <Folder size={18} />
              <span>{t('nav.projects')}</span>
            </button>
            <button className="flex items-center gap-2 text-sm text-slate-400 hover:text-slate-200 transition-colors">
              <HelpCircle size={18} />
              <span>{t('nav.docs')}</span>
            </button>
            <button onClick={onLogout} className="w-8 h-8 rounded-full bg-slate-800 flex items-center justify-center text-slate-300 hover:text-white hover:bg-slate-700 transition-all">
              <User size={18} />
            </button>
          </div>
        </div>
      </header>

      <div className="flex-1 flex overflow-hidden min-h-0">
        <div className="w-64 border-r border-slate-800/50 bg-[#0B1120] flex flex-col py-6 shrink-0">
          <div className="flex flex-col gap-2 px-4">
            <button
              onClick={() => setView('all')}
              className={`flex items-center gap-3 px-4 py-2.5 rounded-lg font-medium w-full text-left ${
                view === 'all' ? 'bg-blue-600/10 text-blue-500' : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/50'
              }`}
            >
              <LayoutGrid size={18} />
              {t('projects.all')}
            </button>
            <button
              onClick={() => setView('recent')}
              className={`flex items-center gap-3 px-4 py-2.5 rounded-lg font-medium w-full text-left ${
                view === 'recent' ? 'bg-blue-600/10 text-blue-500' : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/50'
              }`}
            >
              <Clock size={18} />
              {t('projects.recent')}
            </button>
            <button
              onClick={() => setView('shared')}
              className={`flex items-center gap-3 px-4 py-2.5 rounded-lg font-medium w-full text-left ${
                view === 'shared' ? 'bg-blue-600/10 text-blue-500' : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/50'
              }`}
            >
              <Users size={18} />
              {t('projects.shared')}
            </button>
          </div>

          <div className="mt-12 px-8">
            <div className="text-xs font-semibold text-slate-500 tracking-wider mb-4 uppercase">{t('projects.usage')}</div>
            <div className="h-1.5 w-full bg-slate-800 rounded-full overflow-hidden mb-2">
              <div className="h-full bg-blue-500 rounded-full" style={{ width: `${progress}%` }}></div>
            </div>
            <div className="text-xs text-slate-400">{usageText}</div>
          </div>
        </div>

        <div className="flex-1 overflow-y-auto p-8 min-h-0">
          <div className="max-w-6xl mx-auto">
            <div className="mb-8">
              <h1 className="text-3xl font-bold mb-2">{t('projects.title')}</h1>
              <p className="text-slate-400">{t('projects.subtitle')}</p>
            </div>

            <div className="flex items-center gap-4 mb-6">
              <div className="flex-1 relative">
                <Search size={18} className="absolute left-4 top-1/2 -translate-y-1/2 text-slate-500" />
                <input
                  type="text"
                  value={keyword}
                  onChange={(event) => setKeyword(event.target.value)}
                  placeholder={t('projects.searchPlaceholder')}
                  className="w-full bg-[#1E293B] border border-slate-700 rounded-xl py-2.5 pl-11 pr-4 text-sm text-slate-200 placeholder:text-slate-500 focus:outline-none focus:border-blue-500 transition-colors"
                />
              </div>

              <div className="flex items-center gap-2 px-4 py-2.5 bg-[#1E293B] border border-slate-700 rounded-xl text-sm text-slate-300">
                <Filter size={16} />
                <select
                  value={statusFilter}
                  onChange={(event) => setStatusFilter(event.target.value as StatusFilter)}
                  className="bg-transparent outline-none"
                >
                  {STATUS_FILTERS.map((status) => (
                    <option key={status} value={status} className="bg-[#1E293B]">
                      {status === 'all' ? 'all' : status.toUpperCase()}
                    </option>
                  ))}
                </select>
              </div>

              <button
                onClick={() => setSortOrder((previous) => (previous === 'desc' ? 'asc' : 'desc'))}
                className="flex items-center gap-2 px-4 py-2.5 bg-[#1E293B] border border-slate-700 rounded-xl text-sm font-medium text-slate-300 hover:bg-slate-800 transition-colors"
              >
                <ArrowUpDown size={16} /> {sortOrder === 'desc' ? 'Newest' : 'Oldest'}
              </button>
            </div>

            {error && (
              <div className="mb-6 rounded-xl border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-200 flex items-center justify-between gap-3">
                <span className="flex items-center gap-2">
                  <AlertCircle size={14} /> {error}
                </span>
                <button onClick={() => void loadProjects()} className="px-3 py-1.5 text-xs rounded-md bg-red-500/20 hover:bg-red-500/30">
                  Retry
                </button>
              </div>
            )}

            {loading ? (
              <div className="rounded-2xl border border-slate-800 bg-[#111827] min-h-[260px] flex items-center justify-center text-slate-300 gap-2">
                <Loader2 size={18} className="animate-spin" /> Loading projects...
              </div>
            ) : projects.length === 0 ? (
              <div className="rounded-2xl border border-slate-800 bg-[#111827] min-h-[260px] flex items-center justify-center text-slate-500">
                No projects found.
              </div>
            ) : (
              <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
                {projects.map((project, index) => (
                  <div key={project.id} className="bg-[#1E293B] rounded-2xl border border-slate-800 overflow-hidden flex flex-col group hover:border-slate-600 transition-colors">
                    <div className="h-40 bg-[#17253d] flex items-center justify-center">{fallbackIcon(index)}</div>
                    <div className="p-5 flex flex-col gap-4">
                      <div className="flex items-start justify-between gap-2">
                        <h3 className="font-semibold text-lg truncate">{project.name}</h3>
                        <span className={`text-[10px] font-bold px-2 py-0.5 rounded uppercase tracking-wider shrink-0 ${statusStyle(project.status)}`}>
                          {project.status}
                        </span>
                      </div>
                      <div className="flex items-center gap-2 text-xs text-slate-400">
                        <Clock size={14} />
                        {t('projects.created', { date: formatDate(project.created_at || project.updated_at) })}
                      </div>
                      <div className="flex items-center gap-3 mt-2">
                        <button
                          onClick={() => void openProject(project.id)}
                          disabled={openingProjectId === project.id}
                          className="flex-1 bg-blue-600 hover:bg-blue-700 disabled:opacity-50 text-white py-2 rounded-lg text-sm font-medium transition-colors"
                        >
                          {openingProjectId === project.id ? 'Opening...' : t('projects.openEditor')}
                        </button>
                        <button
                          onClick={() => void removeProject(project.id)}
                          disabled={deletingProjectId === project.id}
                          className="p-2 bg-slate-800 hover:bg-slate-700 disabled:opacity-50 text-slate-300 rounded-lg transition-colors"
                        >
                          {deletingProjectId === project.id ? <Loader2 size={18} className="animate-spin" /> : <Trash2 size={18} />}
                        </button>
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      </div>
    </motion.div>
  );
}
