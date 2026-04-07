import { create } from 'zustand';
import { login as apiLogin } from '@/api';
import { sha256, getToken, setToken, clearToken, getSavedCredentials, saveCredentials, clearCredentials } from '@/hooks/useAuth';

interface AuthState {
  token: string | null;
  loading: boolean;
  error: string | null;
  login: (username: string, password: string, remember: boolean) => Promise<boolean>;
  loginWithHash: (username: string, passHash: string, remember: boolean) => Promise<boolean>;
  logout: () => void;
  restoreSession: () => boolean;
}

export const useAuthStore = create<AuthState>((set) => ({
  token: getToken(),
  loading: false,
  error: null,

  login: async (username, password, remember) => {
    set({ loading: true, error: null });
    try {
      const passHash = await sha256(password);
      const { token } = await apiLogin(username, passHash);
      setToken(token);
      if (remember) {
        saveCredentials(username, passHash);
      } else {
        clearCredentials();
      }
      set({ token, loading: false });
      return true;
    } catch (e: any) {
      set({ loading: false, error: String(e?.message || e) });
      return false;
    }
  },

  loginWithHash: async (username, passHash, remember) => {
    set({ loading: true, error: null });
    try {
      const { token } = await apiLogin(username, passHash);
      setToken(token);
      if (remember) saveCredentials(username, passHash);
      set({ token, loading: false });
      return true;
    } catch (e: any) {
      clearCredentials();
      set({ loading: false, error: null });
      return false;
    }
  },

  logout: () => {
    clearToken();
    // Don't clear saved credentials — "Remember me" should survive logout
    set({ token: null });
  },

  restoreSession: () => {
    const token = getToken();
    if (token) {
      set({ token });
      return true;
    }
    return false;
  },
}));

// Re-export saved credentials helper for login form
export { getSavedCredentials } from '@/hooks/useAuth';
