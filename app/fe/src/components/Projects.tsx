import { motion } from 'motion/react';
import { 
  LayoutGrid, Clock, Users, Search, Filter, ArrowUpDown, 
  Plus, Trash2, MessageSquare, ShoppingCart, LayoutTemplate, BarChart2, ChevronDown, ArrowLeft, Folder, HelpCircle, User
} from 'lucide-react';
import { useI18n } from '../i18n';
import LanguageSwitcher from './LanguageSwitcher';

export default function Projects({ onOpenEditor, onNewApp, onBack, onProjects, onLogout }: { onOpenEditor: () => void, onNewApp: () => void, onBack: () => void, onProjects: () => void, onLogout: () => void }) {
  const { t } = useI18n();

  const projects = [
    {
      id: 1,
      name: t('projects.name1'),
      status: t('status.deployed'),
      date: 'Oct 12, 2023',
      icon: <LayoutGrid size={48} className="text-blue-500" />,
      bg: 'bg-[#1e293b]',
      statusColor: 'text-green-400 bg-green-400/10 border border-green-400/20'
    },
    {
      id: 2,
      name: t('projects.name2'),
      status: t('status.draft'),
      date: 'Oct 15, 2023',
      icon: <ShoppingCart size={48} className="text-orange-500" />,
      bg: 'bg-[#2d1f24]',
      statusColor: 'text-slate-400 bg-slate-400/10 border border-slate-400/20'
    },
    {
      id: 3,
      name: t('projects.name3'),
      status: t('status.deployed'),
      date: 'Nov 02, 2023',
      icon: <MessageSquare size={48} className="text-blue-400" />,
      bg: 'bg-[#17253d]',
      statusColor: 'text-green-400 bg-green-400/10 border border-green-400/20'
    },
    {
      id: 4,
      name: t('projects.name4'),
      status: t('status.draft'),
      date: 'Nov 18, 2023',
      icon: <LayoutTemplate size={48} className="text-emerald-500" />,
      bg: 'bg-[#132a27]',
      statusColor: 'text-slate-400 bg-slate-400/10 border border-slate-400/20'
    },
    {
      id: 5,
      name: t('projects.name5'),
      status: t('status.building'),
      date: 'Dec 05, 2023',
      icon: <BarChart2 size={48} className="text-indigo-500" />,
      bg: 'bg-[#1e1b4b]',
      statusColor: 'text-orange-400 bg-orange-400/10 border border-orange-400/20'
    }
  ];

  return (
    <motion.div 
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      transition={{ duration: 0.3 }}
      className="min-h-screen flex flex-col bg-[#0B1120] text-white font-sans"
    >
      {/* Header */}
      <header className="flex items-center justify-between px-6 py-3 border-b border-slate-800/50 bg-[#0B1120] shrink-0">
        <div className="flex items-center gap-3">
          <button onClick={onBack} className="p-2 -ml-2 text-slate-400 hover:text-white transition-colors rounded-lg hover:bg-slate-800/50">
            <ArrowLeft size={20} />
          </button>
          <div className="w-6 h-6 text-blue-500">
            <svg viewBox="0 0 24 24" fill="currentColor">
              <path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/>
            </svg>
          </div>
          <span className="text-lg font-bold tracking-tight">AI App Gen</span>
        </div>
        <div className="flex items-center gap-6">
          <div className="flex items-center gap-4 border-r border-slate-800 pr-6">
            <button onClick={onNewApp} className="bg-blue-600 hover:bg-blue-700 text-white px-4 py-1.5 rounded-lg text-sm font-medium flex items-center gap-2 transition-colors shadow-lg shadow-blue-500/20">
              <Plus size={16} /> {t('projects.newApp')}
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

      <div className="flex-1 flex overflow-hidden">
        {/* Sidebar */}
        <div className="w-64 border-r border-slate-800/50 bg-[#0B1120] flex flex-col py-6 shrink-0">
          <div className="flex flex-col gap-2 px-4">
            <button className="flex items-center gap-3 px-4 py-2.5 bg-blue-600/10 text-blue-500 rounded-lg font-medium w-full text-left">
              <LayoutGrid size={18} />
              {t('projects.all')}
            </button>
            <button className="flex items-center gap-3 px-4 py-2.5 text-slate-400 hover:text-slate-200 hover:bg-slate-800/50 rounded-lg font-medium w-full text-left transition-colors">
              <Clock size={18} />
              {t('projects.recent')}
            </button>
            <button className="flex items-center gap-3 px-4 py-2.5 text-slate-400 hover:text-slate-200 hover:bg-slate-800/50 rounded-lg font-medium w-full text-left transition-colors">
              <Users size={18} />
              {t('projects.shared')}
            </button>
          </div>

          <div className="mt-12 px-8">
            <div className="text-xs font-semibold text-slate-500 tracking-wider mb-4 uppercase">{t('projects.usage')}</div>
            <div className="h-1.5 w-full bg-slate-800 rounded-full overflow-hidden mb-2">
              <div className="h-full bg-blue-500 w-2/3 rounded-full"></div>
            </div>
            <div className="text-xs text-slate-400">{t('projects.usageCount', { used: 8, limit: 12 })}</div>
          </div>
        </div>

        {/* Main Content */}
        <div className="flex-1 overflow-y-auto p-8">
          <div className="max-w-6xl mx-auto">
            <div className="mb-8">
              <h1 className="text-3xl font-bold mb-2">{t('projects.title')}</h1>
              <p className="text-slate-400">{t('projects.subtitle')}</p>
            </div>

            {/* Controls */}
            <div className="flex items-center gap-4 mb-8">
              <div className="flex-1 relative">
                <Search size={18} className="absolute left-4 top-1/2 -translate-y-1/2 text-slate-500" />
                <input 
                  type="text" 
                  placeholder={t('projects.searchPlaceholder')}
                  className="w-full bg-[#1E293B] border border-slate-700 rounded-xl py-2.5 pl-11 pr-4 text-sm text-slate-200 placeholder:text-slate-500 focus:outline-none focus:border-blue-500 transition-colors"
                />
              </div>
              <button className="flex items-center gap-2 px-4 py-2.5 bg-[#1E293B] border border-slate-700 rounded-xl text-sm font-medium text-slate-300 hover:bg-slate-800 transition-colors">
                <Filter size={16} /> {t('projects.filter')}
              </button>
              <button className="flex items-center gap-2 px-4 py-2.5 bg-[#1E293B] border border-slate-700 rounded-xl text-sm font-medium text-slate-300 hover:bg-slate-800 transition-colors">
                <ArrowUpDown size={16} /> {t('projects.sort')}
              </button>
            </div>

            {/* Grid */}
            <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
              {projects.map(project => (
                <div key={project.id} className="bg-[#1E293B] rounded-2xl border border-slate-800 overflow-hidden flex flex-col group hover:border-slate-600 transition-colors">
                  <div className={`h-40 ${project.bg} flex items-center justify-center`}>
                    {project.icon}
                  </div>
                  <div className="p-5 flex flex-col gap-4">
                    <div className="flex items-start justify-between gap-2">
                      <h3 className="font-semibold text-lg truncate">{project.name}</h3>
                      <span className={`text-[10px] font-bold px-2 py-0.5 rounded uppercase tracking-wider shrink-0 ${project.statusColor}`}>
                        {project.status}
                      </span>
                    </div>
                    <div className="flex items-center gap-2 text-xs text-slate-400">
                      <Clock size={14} />
                      {t('projects.created', { date: project.date })}
                    </div>
                    <div className="flex items-center gap-3 mt-2">
                      <button 
                        onClick={onOpenEditor}
                        className="flex-1 bg-blue-600 hover:bg-blue-700 text-white py-2 rounded-lg text-sm font-medium transition-colors"
                      >
                        {t('projects.openEditor')}
                      </button>
                      <button className="p-2 bg-slate-800 hover:bg-slate-700 text-slate-300 rounded-lg transition-colors">
                        <Trash2 size={18} />
                      </button>
                    </div>
                  </div>
                </div>
              ))}

              {/* Create New App Card */}
              <div 
                onClick={onNewApp}
                className="rounded-2xl border-2 border-dashed border-slate-700 hover:border-slate-500 bg-[#0B1120] flex flex-col items-center justify-center p-8 cursor-pointer group transition-colors min-h-[320px]"
              >
                <div className="w-12 h-12 rounded-full bg-slate-800 flex items-center justify-center text-slate-400 group-hover:text-white group-hover:bg-slate-700 transition-colors mb-4">
                  <Plus size={24} />
                </div>
                <h3 className="font-semibold text-lg mb-1 group-hover:text-white text-slate-300 transition-colors">{t('projects.createNew')}</h3>
                <p className="text-sm text-slate-500 text-center">{t('projects.createNewDesc')}</p>
              </div>
            </div>
          </div>
        </div>
      </div>
    </motion.div>
  );
}
