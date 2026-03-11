/**
 * @license
 * SPDX-License-Identifier: Apache-2.0
 */

import { useState } from 'react';
import { AnimatePresence } from 'motion/react';
import Login from './components/Login';
import Dashboard from './components/Dashboard';
import Workspace from './components/Workspace';
import Projects from './components/Projects';
import { I18nProvider } from './i18n';

function AppContent() {
  const [currentPage, setCurrentPage] = useState<'login' | 'dashboard' | 'workspace' | 'projects'>('login');

  return (
    <div className="min-h-screen bg-[#0B1120] text-white font-sans selection:bg-blue-500/30 overflow-hidden">
      <AnimatePresence mode="wait">
        {currentPage === 'login' && (
          <Login onLogin={() => setCurrentPage('dashboard')} />
        )}
        {currentPage === 'dashboard' && (
          <Dashboard 
            onLogout={() => setCurrentPage('login')} 
            onGenerate={() => setCurrentPage('workspace')} 
            onProjects={() => setCurrentPage('projects')}
          />
        )}
        {currentPage === 'workspace' && (
          <Workspace 
            onBack={() => setCurrentPage('dashboard')} 
            onProjects={() => setCurrentPage('projects')} 
            onLogout={() => setCurrentPage('login')}
          />
        )}
        {currentPage === 'projects' && (
          <Projects 
            onOpenEditor={() => setCurrentPage('workspace')} 
            onNewApp={() => setCurrentPage('dashboard')} 
            onBack={() => setCurrentPage('dashboard')}
            onProjects={() => setCurrentPage('projects')}
            onLogout={() => setCurrentPage('login')}
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
