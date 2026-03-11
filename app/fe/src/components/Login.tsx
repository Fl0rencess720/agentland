import { Github } from 'lucide-react';
import { motion } from 'motion/react';
import { useI18n } from '../i18n';
import LanguageSwitcher from './LanguageSwitcher';

export default function Login({ onLogin }: { onLogin: () => void }) {
  const { t } = useI18n();

  return (
    <motion.div 
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0, y: -20 }}
      transition={{ duration: 0.3 }}
      className="min-h-screen flex flex-col bg-[#0B1120] text-white"
    >
      {/* Navbar */}
      <header className="flex items-center justify-between px-8 py-6">
        <div className="flex items-center gap-3">
          <div className="w-8 h-8 bg-blue-600 rounded-lg flex flex-col items-center justify-center gap-0.5">
             <div className="w-4 h-1 bg-white rounded-full"></div>
             <div className="w-5 h-1 bg-white rounded-full"></div>
             <div className="w-4 h-1 bg-white rounded-full"></div>
          </div>
          <span className="text-xl font-bold tracking-tight">GenAI Pro</span>
        </div>
        <div className="flex items-center gap-3">
          <span className="text-sm text-slate-400">{t('login.authType')}</span>
          <LanguageSwitcher />
        </div>
      </header>

      {/* Main Content */}
      <main className="flex-1 flex items-center justify-center p-4">
        <div className="w-full max-w-[440px] bg-[#111827] border border-slate-800 rounded-2xl p-8 shadow-2xl">
          <div className="text-center mb-8">
            <h1 className="text-3xl font-bold mb-2">{t('login.welcome')}</h1>
            <p className="text-slate-400 text-sm">{t('login.subtitle')}</p>
          </div>

          <div className="space-y-4">
            <button
              onClick={onLogin}
              className="w-full flex items-center justify-center gap-2 bg-transparent border border-slate-700 hover:bg-slate-800 text-white py-3 rounded-lg transition-colors"
            >
              <Github size={18} />
              <span className="text-sm font-medium">{t('login.githubCta')}</span>
            </button>
            <p className="text-center text-xs text-slate-500">
              {t('login.onlyGithub')}
            </p>
          </div>

          <p className="text-center text-xs text-slate-500">
            {t('login.agreementPrefix')}{' '}
            <a href="#" className="underline hover:text-slate-400">{t('nav.terms')}</a>{' '}
            {t('common.and')}{' '}
            <a href="#" className="underline hover:text-slate-400">{t('nav.privacy')}</a>.
          </p>
        </div>
      </main>

      {/* Footer */}
      <footer className="py-6 text-center text-xs text-slate-600">
        {t('login.footer')}
      </footer>
    </motion.div>
  );
}
