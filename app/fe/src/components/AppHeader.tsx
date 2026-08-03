import { useEffect } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { ArrowLeft, Blocks } from 'lucide-react';
import { getCurrentUser, logout } from '../api';
import { queryKeys } from '../queryKeys';
import { useAuthStore } from '../stores/auth';
import LanguageSwitcher from './LanguageSwitcher';
import UserMenu from './UserMenu';

type AppHeaderProps = {
  projectName?: string;
};

export default function AppHeader({ projectName }: AppHeaderProps) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { user, refreshToken, setUser, clear } = useAuthStore();
  const userQuery = useQuery({
    queryKey: queryKeys.currentUser,
    queryFn: getCurrentUser,
    staleTime: 5 * 60_000,
  });

  useEffect(() => {
    if (userQuery.data) setUser(userQuery.data);
    if (userQuery.isError) {
      clear();
      void navigate({ to: '/login', replace: true });
    }
  }, [clear, navigate, setUser, userQuery.data, userQuery.isError]);

  const handleLogout = async () => {
    try {
      if (refreshToken) await logout(refreshToken);
    } finally {
      clear();
      queryClient.clear();
      await navigate({ to: '/login', replace: true });
    }
  };

  return (
    <header className="flex h-14 shrink-0 items-center justify-between gap-3 border-b border-slate-200 bg-white px-3 sm:px-5">
      <div className="flex min-w-0 items-center gap-3">
        {projectName && (
          <Link
            to="/projects"
            aria-label="Back to projects"
            className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-slate-600 hover:bg-slate-100"
          >
            <ArrowLeft size={18} />
          </Link>
        )}
        <Link to="/projects" aria-label="Agentland" className="flex shrink-0 items-center gap-2 text-slate-950">
          <span className="flex h-8 w-8 items-center justify-center rounded-md bg-slate-900 text-white"><Blocks size={17} /></span>
          <span className="hidden text-sm font-semibold sm:block">Agentland</span>
        </Link>
        {projectName && <h1 className="truncate border-l border-slate-200 pl-3 text-sm font-medium text-slate-700">{projectName}</h1>}
      </div>
      <div className="flex shrink-0 items-center gap-2">
        <LanguageSwitcher />
        <UserMenu currentUser={userQuery.data ?? user} onLogout={handleLogout} />
      </div>
    </header>
  );
}
