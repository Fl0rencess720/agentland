import { useEffect, useRef, useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { AlertCircle, Blocks, Github, LoaderCircle } from 'lucide-react';
import { completeGithubAuth, startGithubAuth } from '../api';
import { useI18n } from '../i18n';
import { githubOAuthStatesEqual, saveGitHubOAuthState, takeGitHubOAuthState } from '../oauth';
import { useAuthStore } from '../stores/auth';
import LanguageSwitcher from './LanguageSwitcher';

export default function Login() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const { accessToken, refreshToken, setSession } = useAuthStore();
  const [isAuthenticating, setIsAuthenticating] = useState(false);
  const [error, setError] = useState('');
  const callbackHandled = useRef(false);

  useEffect(() => {
    if ((accessToken || refreshToken) && !window.location.search.includes('code=')) {
      void navigate({ to: '/projects', replace: true });
      return;
    }

    const params = new URLSearchParams(window.location.search);
    const code = params.get('code');
    const state = params.get('state');
    const oauthError = params.get('error_description') || params.get('error');
    if (callbackHandled.current || (!code && !oauthError)) return;
    callbackHandled.current = true;
    window.history.replaceState({}, document.title, '/login');

    const expectedState = takeGitHubOAuthState();
    if (oauthError || !code || !state || !expectedState) {
      setError(oauthError || t('login.error'));
      return;
    }

    setIsAuthenticating(true);
    githubOAuthStatesEqual(state, expectedState)
      .then((matches) => {
        if (!matches) throw new Error(t('login.error'));
        return completeGithubAuth(code, state);
      })
      .then((session) => {
        setSession({
          accessToken: session.access_token,
          refreshToken: session.refresh_token,
          user: session.user,
        });
        return navigate({ to: '/projects', replace: true });
      })
      .catch((cause: Error) => setError(cause.message || t('login.error')))
      .finally(() => setIsAuthenticating(false));
  }, [accessToken, navigate, refreshToken, setSession, t]);

  const beginLogin = async () => {
    setError('');
    setIsAuthenticating(true);
    try {
      const result = await startGithubAuth(`${window.location.origin}/login`);
      saveGitHubOAuthState(result.state);
      window.location.assign(result.authorize_url);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('login.error'));
      setIsAuthenticating(false);
    }
  };

  return (
    <main className="min-h-[100dvh] bg-slate-50 text-slate-950">
      <header className="flex h-16 items-center justify-between border-b border-slate-200 bg-white px-5 sm:px-8">
        <div className="flex items-center gap-2 text-sm font-semibold">
          <span className="flex h-8 w-8 items-center justify-center rounded-md bg-slate-900 text-white"><Blocks size={17} /></span>
          Agentland
        </div>
        <LanguageSwitcher />
      </header>
      <section className="mx-auto flex min-h-[calc(100dvh-4rem)] max-w-md items-center px-5 py-12">
        <div className="w-full rounded-md border border-slate-200 bg-white p-6 shadow-sm sm:p-8">
          <h1 className="text-2xl font-semibold tracking-normal">{t('login.title')}</h1>
          <p className="mt-2 text-sm leading-6 text-slate-600">{t('login.subtitle')}</p>
          <button
            type="button"
            onClick={() => void beginLogin()}
            disabled={isAuthenticating}
            className="mt-7 flex h-11 w-full items-center justify-center gap-2 rounded-md bg-slate-900 px-4 text-sm font-medium text-white hover:bg-slate-800 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {isAuthenticating ? <LoaderCircle size={17} className="animate-spin" /> : <Github size={17} />}
            {isAuthenticating ? t('login.authenticating') : t('login.github')}
          </button>
          {error && (
            <div role="alert" className="mt-4 flex items-start gap-2 rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-800">
              <AlertCircle size={16} className="mt-0.5 shrink-0" />
              <span>{error}</span>
            </div>
          )}
        </div>
      </section>
    </main>
  );
}
