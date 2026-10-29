import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import en from './en.json';
import ko from './ko.json';
import zh from './zh.json';

const LANG_KEY = 'hy-lang';
const SUPPORTED = ['en', 'ko', 'zh'];

const detectLanguage = (): string => {
  try {
    const saved = localStorage.getItem(LANG_KEY);
    if (saved && SUPPORTED.includes(saved)) return saved;
  } catch { /* storage unavailable */ }
  const lang = navigator.language;
  if (lang.startsWith('ko')) return 'ko';
  if (lang.startsWith('zh')) return 'zh';
  return 'en';
};

i18n.use(initReactI18next).init({
  resources: {
    en: { translation: en },
    ko: { translation: ko },
    zh: { translation: zh },
  },
  lng: detectLanguage(),
  fallbackLng: 'en',
  interpolation: {
    escapeValue: false,
  },
});

i18n.on('languageChanged', (lng) => {
  try { localStorage.setItem(LANG_KEY, lng); } catch { /* storage unavailable */ }
});

export default i18n;
