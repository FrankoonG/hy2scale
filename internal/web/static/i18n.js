// i18n — Lightweight internationalization
// Translation files: /i18n/{code}.json
// Usage in HTML: <span data-i18n="key">fallback</span>
// Usage in JS: t('key') or t('key', {port: 80, proto: 'tcp'})

const I18N = {
  lang: localStorage.getItem('hy2scale_lang') || 'en',
  strings: {},
  available: [
    { code: 'en', name: 'English' },
    { code: 'ko', name: '한국어' },
  ],

  async load(code) {
    try {
      const url = (window.__BASE__ || '') + '/i18n/' + code + '.json';
      const r = await fetch(url);
      if (!r.ok) return false;
      I18N.strings = await r.json();
      I18N.lang = code;
      localStorage.setItem('hy2scale_lang', code);
      I18N.apply();
      return true;
    } catch (e) {
      console.error('i18n load error:', e);
      return false;
    }
  },

  // Apply translations to all data-i18n elements
  apply() {
    document.querySelectorAll('[data-i18n]').forEach(el => {
      const key = el.getAttribute('data-i18n');
      const val = I18N.strings[key];
      if (val !== undefined) {
        if (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') {
          el.placeholder = val;
        } else if (el.hasAttribute('data-i18n-html')) {
          el.innerHTML = val;
        } else {
          el.textContent = val;
        }
      }
    });
    // Update page title
    document.title = I18N.strings['app.title'] || 'HY2 SCALE';
  },

  // Get a translated string by key, with optional placeholder replacement
  t(key, params) {
    let s = I18N.strings[key] || key;
    if (params) {
      for (const [k, v] of Object.entries(params)) {
        s = s.replace(new RegExp('\\{' + k + '\\}', 'g'), v);
      }
    }
    return s;
  }
};

// Shorthand
function t(key, params) { return I18N.t(key, params); }
