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
  getJob,
  logout,
  refreshAuthToken,
  sleep,
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
};

const JOB_POLL_INTERVAL_MS = 1500;
const JOB_POLL_MAX_ATTEMPTS = 40;
const ACCESS_TOKEN_KEY = 'access_token';
const REFRESH_TOKEN_KEY = 'refresh_token';

function AppContent() {
  const [currentPage, setCurrentPage] = useState<AppPage>('login');
  const [accessToken, setAccessToken] = useState<string | undefined>(undefined);
  const [refreshToken, setRefreshToken] = useState<string | undefined>(undefined);
  const [currentUser, setCurrentUser] = useState<UserProfile | null>(null);
  const [activeProject, setActiveProject] = useState<ActiveProject | null>(null);

  const [isAuthenticating, setIsAuthenticating] = useState(false);
  const [isRestoringSession, setIsRestoringSession] = useState(true);
  const [authError, setAuthError] = useState<string | null>(null);

  const [isGenerating, setIsGenerating] = useState(false);
  const [generationError, setGenerationError] = useState<string | null>(null);

  useEffect(() => {
    let canceled = false;

    const restoreSession = async () => {
      const storedRefreshToken = localStorage.getItem(REFRESH_TOKEN_KEY);
      if (!storedRefreshToken) {
        if (!canceled) {
          setIsRestoringSession(false);
        }
        return;
      }

      try {
        const refreshed = await refreshAuthToken(storedRefreshToken);
        const user = await getCurrentUser(refreshed.access_token);

        if (canceled) {
          return;
        }

        localStorage.setItem(ACCESS_TOKEN_KEY, refreshed.access_token);
        localStorage.setItem(REFRESH_TOKEN_KEY, refreshed.refresh_token);
        setAccessToken(refreshed.access_token);
        setRefreshToken(refreshed.refresh_token);
        setCurrentUser(user);
        setCurrentPage('dashboard');
      } catch {
        localStorage.removeItem(ACCESS_TOKEN_KEY);
        localStorage.removeItem(REFRESH_TOKEN_KEY);
        if (!canceled) {
          setAccessToken(undefined);
          setRefreshToken(undefined);
          setCurrentUser(null);
          setCurrentPage('login');
        }
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

  const waitForGeneration = async (jobId: string, token?: string) => {
    for (let attempt = 0; attempt < JOB_POLL_MAX_ATTEMPTS; attempt += 1) {
      const job = await getJob(jobId, token);

      if (job.status === 'SUCCESS') {
        return job;
      }

      if (job.status === 'FAILED' || job.status === 'CANCELED') {
        throw new Error(`Generation ${job.status.toLowerCase()}`);
      }

      await sleep(JOB_POLL_INTERVAL_MS);
    }

    throw new Error('Generation timeout');
  };

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

      const generation = await createGeneration(project.id, normalizedPrompt, attachments, accessToken);
      await waitForGeneration(generation.job_id, accessToken);

      setActiveProject({
        id: project.id,
        name: project.name,
        prompt: normalizedPrompt,
        viewMode: 'preview',
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
      const redirectUri = `${window.location.origin}/auth/github/callback`;
      const start = await startGithubAuth(redirectUri);
      const callback = await completeGithubAuth('mock_github_code', start.state);
      const user = await getCurrentUser(callback.access_token);

      localStorage.setItem(ACCESS_TOKEN_KEY, callback.access_token);
      localStorage.setItem(REFRESH_TOKEN_KEY, callback.refresh_token);
      setAccessToken(callback.access_token);
      setRefreshToken(callback.refresh_token);
      setCurrentUser(user);
      setGenerationError(null);
      setCurrentPage('dashboard');
    } catch (error) {
      setAuthError((error as Error).message || 'Failed to authenticate');
    } finally {
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
      // Clear local session even if mock logout fails.
    } finally {
      localStorage.removeItem(ACCESS_TOKEN_KEY);
      localStorage.removeItem(REFRESH_TOKEN_KEY);
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
            accessToken={accessToken}
          />
        )}

        {currentPage === 'projects' && (
          <Projects
            onOpenEditor={openProjectInWorkspace}
            onBack={goDashboard}
            onProjects={() => setCurrentPage('projects')}
            onLogout={handleLogout}
            accessToken={accessToken}
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
