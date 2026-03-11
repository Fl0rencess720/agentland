import { motion } from 'motion/react';
import { HelpCircle, User, Sparkles, Paperclip, Settings, Zap, Folder } from 'lucide-react';
import { useI18n } from '../i18n';
import LanguageSwitcher from './LanguageSwitcher';

export default function Dashboard({ onLogout, onGenerate, onProjects }: { onLogout: () => void, onGenerate: () => void, onProjects: () => void }) {
  const { t } = useI18n();

  return (
    <motion.div 
      initial={{ opacity: 0, y: 20 }}
      animate={{ opacity: 1, y: 0 }}
      exit={{ opacity: 0, scale: 0.98 }}
      transition={{ duration: 0.4, ease: [0.16, 1, 0.3, 1] }}
      className="min-h-screen flex flex-col bg-[#0B1120] text-white"
    >
      {/* Navbar */}
      <header className="flex items-center justify-between px-6 py-3 border-b border-slate-800/50 bg-[#0B1120] shrink-0">
        <div className="flex items-center gap-3">
          <div className="w-6 h-6 text-blue-500">
            <svg viewBox="0 0 24 24" fill="currentColor">
              <path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/>
            </svg>
          </div>
          <span className="text-lg font-bold tracking-tight">AI App Gen</span>
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
      </header>

      {/* Main Content */}
      <main className="flex-1 max-w-6xl w-full mx-auto p-6 flex flex-col gap-6">
        <div className="mt-4">
          <h1 className="text-3xl font-bold mb-2">{t('dashboard.title')}</h1>
          <p className="text-slate-400">{t('dashboard.subtitle')}</p>
        </div>

        {/* Prompt Box */}
        <div className="bg-[#111827] border border-slate-800 rounded-2xl p-4 flex flex-col gap-4 shadow-lg">
          <div className="flex gap-4">
            <div className="w-10 h-10 rounded-xl bg-blue-500/10 flex items-center justify-center shrink-0">
              <Sparkles className="text-blue-500" size={20} />
            </div>
            <textarea 
              className="w-full bg-transparent text-slate-300 placeholder:text-slate-600 resize-none outline-none py-2 min-h-[80px]"
              placeholder={t('dashboard.promptPlaceholder')}
            ></textarea>
          </div>
          
          <div className="flex items-center justify-between pt-2 border-t border-slate-800/50">
            <div className="flex items-center gap-4 text-slate-500">
              <button className="hover:text-slate-300 transition-colors"><Paperclip size={18} /></button>
              <button className="hover:text-slate-300 transition-colors"><Settings size={18} /></button>
            </div>
            <button onClick={onGenerate} className="bg-blue-600 hover:bg-blue-700 text-white px-5 py-2.5 rounded-xl font-medium flex items-center gap-2 transition-colors shadow-lg shadow-blue-500/20">
              {t('dashboard.generate')} <Zap size={16} className="fill-current" />
            </button>
          </div>
        </div>
      </main>

      {/* Footer */}
      <footer className="px-6 py-4 flex items-center justify-between text-xs text-slate-500 border-t border-slate-800">
        <div className="flex items-center gap-4">
          <div className="flex items-center gap-2">
            <div className="w-2 h-2 rounded-full bg-green-500"></div>
            <span>{t('dashboard.systemOnline')}</span>
          </div>
          <div className="w-px h-3 bg-slate-700"></div>
          <span>{t('dashboard.version')}</span>
        </div>
        <div className="flex items-center gap-6">
          <a href="#" className="hover:text-slate-300">{t('nav.privacy')}</a>
          <a href="#" className="hover:text-slate-300">{t('nav.terms')}</a>
          <div className="flex items-center gap-1 bg-slate-800/50 px-2 py-1 rounded border border-slate-700/50">
            <span className="font-mono">⌘ + K</span>
            <span className="ml-1">{t('dashboard.commandPalette')}</span>
          </div>
        </div>
      </footer>
    </motion.div>
  );
}
