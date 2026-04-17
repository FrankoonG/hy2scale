import { create } from 'zustand';
import { login as apiLogin } from '@/api';
import { sha256, getToken, setToken, clearToken, getSavedCredentials, saveCredentials, clearCredentials, setSessionHash, clearSessionHash } from '@/hooks/useAuth';

const isProxy = () => !!(window as any).__PROXY__;

interface AuthState {
  token: string | null;
  loading: boolean;
  error: string | null;
  forcePasswordChange: boolean;
  login: (username: string, password: string, remember: boolean) => Promise<boolean>;
  loginWithHash: (username: string, passHash: string, remember: boolean) => Promise<boolean>;
  logout: () => void;
  restoreSession: () => boolean;
  clearForcePasswordChange: () => void;
}

export const useAuthStore = create<AuthState>((set) => ({
  token: getToken(),
  loading: false,
  error: null,
  forcePasswordChange: false,

  login: async (username, password, remember) => {
    set({ loading: true, error: null });
    try {
      const passHash = await sha256(password);
      const res = await apiLogin(username, passHash);
      setToken(res.token);
      if (remember) {
        saveCredentials(username, passHash);
      } else {
        clearCredentials();
      }
      // Remember this tab's hash when logging in to the local node so we can
      // offer auto-login on remote nodes that share credentials, regardless
      // of whether "remember me" is ticked.
      if (!isProxy()) setSessionHash(username, passHash);
      set({ token: res.token, loading: false, forcePasswordChange: !!res.force_password_change });
      return true;
    } catch (e: any) {
      set({ loading: false, error: String(e?.message || e) });
      return false;
    }
  },

  loginWithHash: async (username, passHash, remember) => {
    set({ loading: true, error: null });
    try {
      const res = await apiLogin(username, passHash);
      setToken(res.token);
      if (remember) saveCredentials(username, passHash);
      if (!isProxy()) setSessionHash(username, passHash);
      set({ token: res.token, loading: false, forcePasswordChange: !!res.force_password_change });
      return true;
    } catch (e: any) {
      clearCredentials();
      set({ loading: false, error: null });
      return false;
    }
  },

  logout: () => {
    clearToken();
    if (!isProxy()) clearSessionHash();
    set({ token: null, forcePasswordChange: false });
  },

  restoreSession: () => {
    const token = getToken();
    if (token) {
      set({ token });
      return true;
    }
    return false;
  },

  clearForcePasswordChange: () => set({ forcePasswordChange: false }),
}));

// Re-export saved credentials helper for login form
export { getSavedCredentials } from '@/hooks/useAuth';
