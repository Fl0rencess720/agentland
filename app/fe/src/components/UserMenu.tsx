import { useEffect, useMemo, useRef, useState } from 'react';
import { ChevronDown, LogOut, User } from 'lucide-react';
import type { UserProfile } from '../api';
import { useI18n } from '../i18n';

type UserMenuProps = {
  currentUser: UserProfile | null;
  onLogout: () => Promise<void> | void;
};

function initials(user: UserProfile | null) {
  return (user?.name?.trim() || user?.email?.trim() || 'U').slice(0, 1).toUpperCase();
}

export default function UserMenu({ currentUser, onLogout }: UserMenuProps) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const avatar = useMemo(() => initials(currentUser), [currentUser]);

  useEffect(() => {
    const closeOutside = (event: MouseEvent) => {
      if (!containerRef.current?.contains(event.target as Node)) setOpen(false);
    };
    const closeEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', closeOutside);
    document.addEventListener('keydown', closeEscape);
    return () => {
      document.removeEventListener('mousedown', closeOutside);
      document.removeEventListener('keydown', closeEscape);
    };
  }, []);

  return (
    <div ref={containerRef} className="relative">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        aria-expanded={open}
        className="flex h-9 items-center gap-2 rounded-md border border-slate-200 bg-white px-2 text-slate-700 hover:bg-slate-50"
      >
        {currentUser?.avatar_url ? (
          <img src={currentUser.avatar_url} alt="" className="h-6 w-6 rounded-full object-cover" />
        ) : (
          <span className="flex h-6 w-6 items-center justify-center rounded-full bg-slate-900 text-xs font-semibold text-white">{avatar}</span>
        )}
        <span className="hidden max-w-32 truncate text-sm sm:block">{currentUser?.name || currentUser?.email || t('account.fallback')}</span>
        <ChevronDown size={14} aria-hidden="true" />
      </button>

      {open && (
        <div className="absolute right-0 z-50 mt-2 w-64 rounded-md border border-slate-200 bg-white shadow-lg">
          <div className="border-b border-slate-100 p-3">
            <div className="flex items-center gap-2 text-sm font-medium text-slate-900"><User size={15} />{currentUser?.name || t('account.fallback')}</div>
            <div className="mt-1 truncate text-xs text-slate-500">{currentUser?.email}</div>
          </div>
          <button
            type="button"
            onClick={() => {
              setOpen(false);
              void onLogout();
            }}
            className="flex w-full items-center gap-2 rounded-b-md px-3 py-2.5 text-left text-sm text-red-700 hover:bg-red-50"
          >
            <LogOut size={15} />
            {t('account.logout')}
          </button>
        </div>
      )}
    </div>
  );
}
