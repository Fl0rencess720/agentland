import { useEffect, useMemo, useRef, useState } from 'react';
import { ChevronDown, LogOut, User } from 'lucide-react';
import type { UserProfile } from '../api';

type UserMenuProps = {
  currentUser: UserProfile | null;
  onLogout: () => Promise<void> | void;
};

function initialsFromUser(user: UserProfile | null) {
  const seed = user?.name?.trim() || user?.email?.trim() || 'U';
  return seed.slice(0, 1).toUpperCase();
}

export default function UserMenu({ currentUser, onLogout }: UserMenuProps) {
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const handlePointerDown = (event: MouseEvent) => {
      if (!containerRef.current?.contains(event.target as Node)) {
        setOpen(false);
      }
    };

    const handleEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false);
      }
    };

    document.addEventListener('mousedown', handlePointerDown);
    document.addEventListener('keydown', handleEscape);
    return () => {
      document.removeEventListener('mousedown', handlePointerDown);
      document.removeEventListener('keydown', handleEscape);
    };
  }, []);

  const avatar = useMemo(() => initialsFromUser(currentUser), [currentUser]);

  return (
    <div ref={containerRef} className="relative">
      <button
        type="button"
        onClick={() => setOpen((previous) => !previous)}
        title={currentUser?.name ?? currentUser?.email ?? 'Account'}
        className="flex items-center gap-2 rounded-full border border-slate-700 bg-slate-800/80 px-2 py-1 text-slate-200 transition hover:border-slate-600 hover:bg-slate-800"
      >
        {currentUser?.avatar_url ? (
          <img src={currentUser.avatar_url} alt={currentUser.name || 'User avatar'} className="h-8 w-8 rounded-full object-cover" />
        ) : (
          <span className="flex h-8 w-8 items-center justify-center rounded-full bg-blue-600 text-sm font-semibold text-white">
            {avatar}
          </span>
        )}
        <span className="hidden max-w-32 truncate text-sm text-slate-300 md:block">
          {currentUser?.name || currentUser?.email || 'Account'}
        </span>
        <ChevronDown size={16} className={`text-slate-400 transition ${open ? 'rotate-180' : ''}`} />
      </button>

      {open && (
        <div className="absolute right-0 z-20 mt-2 w-64 overflow-hidden rounded-2xl border border-slate-700 bg-[#111827] shadow-2xl shadow-black/30">
          <div className="border-b border-slate-800 px-4 py-3">
            <div className="flex items-center gap-3">
              {currentUser?.avatar_url ? (
                <img src={currentUser.avatar_url} alt={currentUser.name || 'User avatar'} className="h-10 w-10 rounded-full object-cover" />
              ) : (
                <div className="flex h-10 w-10 items-center justify-center rounded-full bg-blue-600 text-sm font-semibold text-white">
                  {avatar}
                </div>
              )}
              <div className="min-w-0">
                <div className="truncate text-sm font-medium text-white">
                  {currentUser?.name || 'GitHub User'}
                </div>
                <div className="truncate text-xs text-slate-400">
                  {currentUser?.email || 'No email'}
                </div>
              </div>
            </div>
            <div className="mt-3 inline-flex items-center gap-2 rounded-full border border-blue-500/30 bg-blue-500/10 px-2.5 py-1 text-xs text-blue-200">
              <User size={12} />
              <span>{(currentUser?.plan || 'free').toUpperCase()}</span>
            </div>
          </div>

          <button
            type="button"
            onClick={() => {
              setOpen(false);
              void onLogout();
            }}
            className="flex w-full items-center gap-2 px-4 py-3 text-left text-sm text-red-300 transition hover:bg-red-500/10"
          >
            <LogOut size={16} />
            <span>Log out</span>
          </button>
        </div>
      )}
    </div>
  );
}
