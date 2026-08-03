import { create } from 'zustand';
import type { UserProfile } from '../api';

const ACCESS_TOKEN_KEY = 'access_token';
const REFRESH_TOKEN_KEY = 'refresh_token';
const USER_PROFILE_KEY = 'current_user';

type AuthSession = {
  accessToken: string;
  refreshToken: string;
  user: UserProfile | null;
};

type AuthState = AuthSession & {
  setSession: (session: AuthSession) => void;
  setUser: (user: UserProfile | null) => void;
  setTokens: (accessToken: string, refreshToken: string) => void;
  clear: () => void;
};

function readStoredUser(): UserProfile | null {
  const raw = localStorage.getItem(USER_PROFILE_KEY);
  if (!raw) return null;

  try {
    return JSON.parse(raw) as UserProfile;
  } catch {
    localStorage.removeItem(USER_PROFILE_KEY);
    return null;
  }
}

function readInitialSession(): AuthSession {
  if (typeof window === 'undefined') {
    return { accessToken: '', refreshToken: '', user: null };
  }

  return {
    accessToken: localStorage.getItem(ACCESS_TOKEN_KEY) ?? '',
    refreshToken: localStorage.getItem(REFRESH_TOKEN_KEY) ?? '',
    user: readStoredUser(),
  };
}

function persistSession(session: AuthSession) {
  if (session.accessToken) localStorage.setItem(ACCESS_TOKEN_KEY, session.accessToken);
  else localStorage.removeItem(ACCESS_TOKEN_KEY);

  if (session.refreshToken) localStorage.setItem(REFRESH_TOKEN_KEY, session.refreshToken);
  else localStorage.removeItem(REFRESH_TOKEN_KEY);

  if (session.user) localStorage.setItem(USER_PROFILE_KEY, JSON.stringify(session.user));
  else localStorage.removeItem(USER_PROFILE_KEY);
}

const initialSession = readInitialSession();

export const useAuthStore = create<AuthState>((set) => ({
  ...initialSession,
  setSession: (session) => {
    persistSession(session);
    set(session);
  },
  setUser: (user) => {
    const current = useAuthStore.getState();
    persistSession({ accessToken: current.accessToken, refreshToken: current.refreshToken, user });
    set({ user });
  },
  setTokens: (accessToken, refreshToken) => {
    const current = useAuthStore.getState();
    persistSession({ accessToken, refreshToken, user: current.user });
    set({ accessToken, refreshToken });
  },
  clear: () => {
    persistSession({ accessToken: '', refreshToken: '', user: null });
    set({ accessToken: '', refreshToken: '', user: null });
  },
}));

export function getAuthSession() {
  const { accessToken, refreshToken, user } = useAuthStore.getState();
  return { accessToken, refreshToken, user };
}
