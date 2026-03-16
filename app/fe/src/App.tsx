/**
 * @license
 * SPDX-License-Identifier: Apache-2.0
 */

import { useEffect, useState } from 'react';
import { AnimatePresence } from 'motion/react';
import Login from './components/Login';
import Dashboard from './components/Dashboard';
import Workspace from './components/Workspace';
import Projects from './components/Projects';
import { I18nProvider } from './i18n';
import {
  completeGithubAuth,
  createGeneration,
  createProject,
  getCurrentUser,
  logout,
  refreshAuthToken,
  startGithubAuth,
  uploadImageAttachment,
  type GenerationAttachment,
  type UserProfile,
} from './api';

type AppPage = 'login' | 'dashboard' | 'workspace' | 'projects';

type ActiveProject = {
  id: string;
  name: string;
  prompt: string;
  viewMode: 'preview' | 'code';
  generationJobId?: string;
};

type AuthBootstrapState = {
  accessToken: string;
  refreshToken: string;
  user: UserProfile;
};

const ACCESS_TOKEN_KEY = 'access_token';
const REFRESH_TOKEN_KEY = 'refresh_token';
const USER_PROFILE_KEY = 'current_user';
const DEEP_ENABLED_KEY = 'deep_enabled';
const AUTH_CALLBACK_PATH = '/auth/github/callback';

let pendingOAuthBootstrap: Promise<AuthBootstrapState> | null = null;

function persistSession(session: AuthBootstrapState) {
  localStorage.setItem(ACCESS_TOKEN_KEY, session.accessToken);
  localStorage.setItem(REFRESH_TOKEN_KEY, session.refreshToken);
  localStorage.setItem(USER_PROFILE_KEY, JSON.stringify(session.user));
}

function clearStoredSession() {
  localStorage.removeItem(ACCESS_TOKEN_KEY);
  localStorage.removeItem(REFRESH_TOKEN_KEY);
  localStorage.removeItem(USER_PROFILE_KEY);
}

function readStoredUser(): UserProfile | null {
  const raw = localStorage.getItem(USER_PROFILE_KEY);
  if (!raw) {
    return null;
  }

  try {
    return JSON.parse(raw) as UserProfile;
  } catch {
    localStorage.removeItem(USER_PROFILE_KEY);
    return null;
  }
}

function readStoredDeepEnabled(): boolean {
  if (typeof window === 'undefined') {
    return false;
  }
  return window.localStorage.getItem(DEEP_ENABLED_KEY) === 'true';
}

async function bootstrapOAuthSession(code: string, state: string): Promise<AuthBootstrapState> {
  const callback = await completeGithubAuth(code, state);
  const user = await getCurrentUser(callback.access_token);
  const session = {
    accessToken: callback.access_token,
    refreshToken: callback.refresh_token,
    user,
  };

  persistSession(session);
  return session;
}

function AppContent() {
  const [currentPage, setCurrentPage] = useState<AppPage>('login');
  const [accessToken, setAccessToken] = useState<string | undefined>(undefined);
  const [refreshToken, setRefreshToken] = useState<string | undefined>(undefined);
  const [currentUser, setCurrentUser] = useState<UserProfile | null>(null);
  const [activeProject, setActiveProject] = useState<ActiveProject | null>(null);
  const [deepEnabled, setDeepEnabled] = useState<boolean>(readStoredDeepEnabled);

  const [isAuthenticating, setIsAuthenticating] = useState(false);
  const [isRestoringSession, setIsRestoringSession] = useState(true);
  const [authError, setAuthError] = useState<string | null>(null);

  const [isGenerating, setIsGenerating] = useState(false);
  const [generationError, setGenerationError] = useState<string | null>(null);

  useEffect(() => {
    window.localStorage.setItem(DEEP_ENABLED_KEY, deepEnabled ? 'true' : 'false');
  }, [deepEnabled]);

  useEffect(() => {
    let canceled = false;

    const applySession = (session: AuthBootstrapState) => {
      if (canceled) {
        return;
      }
      setAccessToken(session.accessToken);
      setRefreshToken(session.refreshToken);
      setCurrentUser(session.user);
      setGenerationError(null);
      setCurrentPage('dashboard');
    };

    const resetToLogin = (message?: string) => {
      clearStoredSession();
      if (canceled) {
        return;
      }
      setAccessToken(undefined);
      setRefreshToken(undefined);
      setCurrentUser(null);
      setActiveProject(null);
      setCurrentPage('login');
      if (message) {
        setAuthError(message);
      }
    };

    const completeOAuthCallback = async () => {
      if (window.location.pathname !== AUTH_CALLBACK_PATH) {
        return false;
      }

      const callbackSearch = window.location.search;
      window.history.replaceState({}, document.title, '/');

      const searchParams = new URLSearchParams(callbackSearch);
      const code = searchParams.get('code');
      const state = searchParams.get('state');
      const oauthError = searchParams.get('error');
      const oauthErrorDescription = searchParams.get('error_description');

      if (oauthError) {
        resetToLogin(oauthErrorDescription || oauthError);
        return true;
      }

      if (!code || !state) {
        resetToLogin('Missing GitHub callback parameters');
        return true;
      }

      if (!canceled) {
        setIsAuthenticating(true);
        setAuthError(null);
      }

      try {
        pendingOAuthBootstrap ??= bootstrapOAuthSession(code, state);
        const session = await pendingOAuthBootstrap;
        applySession(session);
      } catch (error) {
        pendingOAuthBootstrap = null;
        resetToLogin((error as Error).message || 'Failed to authenticate');
      } finally {
        pendingOAuthBootstrap = null;
        if (!canceled) {
          setIsAuthenticating(false);
          setIsRestoringSession(false);
        }
      }

      return true;
    };

    const restoreSession = async () => {
      const handledCallback = await completeOAuthCallback();
      if (handledCallback) {
        return;
      }

      if (pendingOAuthBootstrap) {
        try {
          const session = await pendingOAuthBootstrap;
          applySession(session);
        } catch (error) {
          resetToLogin((error as Error).message || 'Failed to authenticate');
        } finally {
          pendingOAuthBootstrap = null;
          if (!canceled) {
            setIsRestoringSession(false);
          }
        }
        return;
      }

      const storedAccessToken = localStorage.getItem(ACCESS_TOKEN_KEY) ?? undefined;
      const storedRefreshToken = localStorage.getItem(REFRESH_TOKEN_KEY) ?? undefined;
      const storedUser = readStoredUser();

      if (!storedAccessToken && !storedRefreshToken) {
        if (!canceled) {
          setCurrentUser(storedUser);
          setIsRestoringSession(false);
        }
        return;
      }

      if (storedAccessToken) {
        try {
          const user = await getCurrentUser(storedAccessToken);
          const session = {
            accessToken: storedAccessToken,
            refreshToken: storedRefreshToken ?? '',
            user,
          };
          localStorage.setItem(USER_PROFILE_KEY, JSON.stringify(user));
          applySession(session);
          if (!canceled) {
            setIsRestoringSession(false);
          }
          return;
        } catch {
          localStorage.removeItem(ACCESS_TOKEN_KEY);
        }
      }

      if (!storedRefreshToken) {
        resetToLogin();
        if (!canceled) {
          setIsRestoringSession(false);
        }
        return;
      }

      try {
        const refreshed = await refreshAuthToken(storedRefreshToken);
        const user = await getCurrentUser(refreshed.access_token);
        const session = {
          accessToken: refreshed.access_token,
          refreshToken: refreshed.refresh_token,
          user,
        };
        persistSession(session);
        applySession(session);
      } catch (error) {
        resetToLogin((error as Error).message || undefined);
      } finally {
        if (!canceled) {
          setIsRestoringSession(false);
        }
      }
    };

    void restoreSession();

    return () => {
      canceled = true;
    };
  }, []);


  const handleGenerate = async (prompt: string, attachments: GenerationAttachment[] = []) => {
    const normalizedPrompt = prompt.trim();
    if (!normalizedPrompt || isGenerating) {
      return;
    }

    setIsGenerating(true);
    setGenerationError(null);

    try {
      const project = await createProject(
        {
          name: 'Untitled Project',
          template: 'blank',
        },
        accessToken,
      );

      const generation = await createGeneration(project.id, normalizedPrompt, attachments, accessToken, deepEnabled);

      setActiveProject({
        id: project.id,
        name: project.name,
        prompt: normalizedPrompt,
        viewMode: 'preview',
        generationJobId: generation.job_id,
      });
      setCurrentPage('workspace');
    } catch (error) {
      setGenerationError((error as Error).message || 'Failed to generate app');
    } finally {
      setIsGenerating(false);
    }
  };

  const handleUploadImage = async (file: File) => {
    return uploadImageAttachment(file, accessToken);
  };

  const handleLogin = async () => {
    setIsAuthenticating(true);
    setAuthError(null);

    try {
      const redirectUri = `${window.location.origin}${AUTH_CALLBACK_PATH}`;
      const start = await startGithubAuth(redirectUri);
      window.location.assign(start.authorize_url);
    } catch (error) {
      setAuthError((error as Error).message || 'Failed to authenticate');
      setIsAuthenticating(false);
    }
  };

  const handleLogout = async () => {
    try {
      const storedRefreshToken = refreshToken ?? localStorage.getItem(REFRESH_TOKEN_KEY) ?? undefined;
      const storedAccessToken = accessToken ?? localStorage.getItem(ACCESS_TOKEN_KEY) ?? undefined;
      if (storedRefreshToken) {
        await logout(storedRefreshToken, storedAccessToken);
      }
    } catch {
      // Clear local session even if logout fails.
    } finally {
      pendingOAuthBootstrap = null;
      clearStoredSession();
      setIsAuthenticating(false);
      setIsRestoringSession(false);
      setAuthError(null);
      setAccessToken(undefined);
      setRefreshToken(undefined);
      setCurrentUser(null);
      setActiveProject(null);
      setCurrentPage('login');
    }
  };

  const goDashboard = () => setCurrentPage('dashboard');
  const openProjectInWorkspace = (project: { id: string; name: string; viewMode?: 'preview' | 'code' }) => {
    setActiveProject({
      id: project.id,
      name: project.name,
      prompt: '',
      viewMode: project.viewMode ?? 'preview',
      generationJobId: undefined,
    });
    setCurrentPage('workspace');
  };

  return (
    <div className="min-h-screen bg-[#0B1120] text-white font-sans selection:bg-blue-500/30 overflow-hidden">
      <AnimatePresence mode="wait">
        {currentPage === 'login' && (
          <Login
            onLogin={handleLogin}
            isAuthenticating={isAuthenticating || isRestoringSession}
            authError={authError}
          />
        )}

        {currentPage === 'dashboard' && (
          <Dashboard
            onLogout={handleLogout}
            onGenerate={handleGenerate}
            onProjects={() => setCurrentPage('projects')}
            onUploadImage={handleUploadImage}
            deepEnabled={deepEnabled}
            onDeepEnabledChange={setDeepEnabled}
            isGenerating={isGenerating}
            generationError={generationError}
            currentUser={currentUser}
          />
        )}

        {currentPage === 'workspace' && activeProject && (
          <Workspace
            onBack={goDashboard}
            onProjects={() => setCurrentPage('projects')}
            onLogout={handleLogout}
            projectId={activeProject.id}
            projectName={activeProject.name}
            initialPrompt={activeProject.prompt}
            initialViewMode={activeProject.viewMode}
            generationJobId={activeProject.generationJobId}
            accessToken={accessToken}
            deepEnabled={deepEnabled}
            onDeepEnabledChange={setDeepEnabled}
            currentUser={currentUser}
          />
        )}

        {currentPage === 'projects' && (
          <Projects
            onOpenEditor={openProjectInWorkspace}
            onBack={goDashboard}
            onProjects={() => setCurrentPage('projects')}
            onLogout={handleLogout}
            accessToken={accessToken}
            currentUser={currentUser}
          />
        )}
      </AnimatePresence>
    </div>
  );
}

export default function App() {
  return (
    <I18nProvider>
      <AppContent />
    </I18nProvider>
  );
}
