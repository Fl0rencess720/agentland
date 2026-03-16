import { Github, Loader2, AlertCircle } from 'lucide-react';
import { motion } from 'motion/react';
import { useI18n } from '../i18n';
import LanguageSwitcher from './LanguageSwitcher';

type LoginProps = {
  onLogin: () => Promise<void> | void;
  isAuthenticating: boolean;
  authError: string | null;
};

export default function Login({ onLogin, isAuthenticating, authError }: LoginProps) {
  const { t } = useI18n();

  return (
    <motion.div
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0, y: -20 }}
      transition={{ duration: 0.3 }}
      className="min-h-screen flex flex-col bg-[#0B1120] text-white"
    >
      <header className="flex items-center justify-between px-8 py-6">
        <div className="flex items-center gap-3">
          <div className="w-8 h-8 bg-blue-600 rounded-lg flex flex-col items-center justify-center gap-0.5">
            <div className="w-4 h-1 bg-white rounded-full"></div>
            <div className="w-5 h-1 bg-white rounded-full"></div>
            <div className="w-4 h-1 bg-white rounded-full"></div>
          </div>
          <span className="text-xl font-bold tracking-tight">Agentland</span>
        </div>
        <div className="flex items-center gap-3">
          <LanguageSwitcher />
        </div>
      </header>

      <main className="flex-1 flex items-center justify-center p-4">
        <div className="w-full max-w-[440px] bg-[#111827] border border-slate-800 rounded-2xl p-8 shadow-2xl">
          <div className="text-center mb-8">
            <h1 className="text-3xl font-bold mb-2">{t('login.welcome')}</h1>
            <p className="text-slate-400 text-sm">{t('login.subtitle')}</p>
          </div>

          <div className="space-y-4">
            <button
              onClick={onLogin}
              disabled={isAuthenticating}
              className="w-full flex items-center justify-center gap-2 bg-transparent border border-slate-700 hover:bg-slate-800 disabled:opacity-50 disabled:cursor-not-allowed text-white py-3 rounded-lg transition-colors"
            >
              {isAuthenticating ? <Loader2 size={18} className="animate-spin" /> : <Github size={18} />}
              <span className="text-sm font-medium">{isAuthenticating ? 'Signing in...' : t('login.githubCta')}</span>
            </button>
            <p className="text-center text-xs text-slate-500">{t('login.onlyGithub')}</p>
          </div>

          {authError && (
            <div className="mt-4 rounded-lg px-3 py-2 text-sm border bg-red-500/10 text-red-300 border-red-500/30">
              <span className="flex items-center gap-2">
                <AlertCircle size={14} /> {authError}
              </span>
            </div>
          )}

          <p className="text-center text-xs text-slate-500 mt-6">
            {t('login.agreementPrefix')}{' '}
            <a href="#" className="underline hover:text-slate-400">{t('nav.terms')}</a>{' '}
            {t('common.and')}{' '}
            <a href="#" className="underline hover:text-slate-400">{t('nav.privacy')}</a>.
          </p>
        </div>
      </main>

      <footer className="py-6 text-center text-xs text-slate-600">{t('login.footer')}</footer>
    </motion.div>
  );
}
