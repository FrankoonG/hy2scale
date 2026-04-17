import { getBasePath } from '@/api/client';

const tokenKey = () => 'token:' + getBasePath();
const credKey = 'hy2scale_cred';
// Session hash stored on successful local (non-proxy) login. Used to
// auto-login when the user visits a remote node whose password matches,
// even without the "remember me" box being ticked.
const sessionHashKey = 'hy2scale_session_hash';

export function getToken(): string | null {
  return sessionStorage.getItem(tokenKey());
}

export function setToken(token: string) {
  sessionStorage.setItem(tokenKey(), token);
}

export function clearToken() {
  sessionStorage.removeItem(tokenKey());
}

export function getSessionHash(): { u: string; h: string } | null {
  try {
    const raw = sessionStorage.getItem(sessionHashKey);
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
}

export function setSessionHash(username: string, passHash: string) {
  sessionStorage.setItem(sessionHashKey, JSON.stringify({ u: username, h: passHash }));
}

export function clearSessionHash() {
  sessionStorage.removeItem(sessionHashKey);
}

export function getSavedCredentials(): { u: string; h: string } | null {
  try {
    const raw = localStorage.getItem(credKey);
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
}

export function saveCredentials(username: string, passHash: string) {
  localStorage.setItem(credKey, JSON.stringify({ u: username, h: passHash }));
}

export function clearCredentials() {
  localStorage.removeItem(credKey);
}

export async function sha256(message: string): Promise<string> {
  // Use Web Crypto API if available (HTTPS or localhost)
  if (typeof crypto !== 'undefined' && crypto.subtle) {
    const data = new TextEncoder().encode(message);
    const hash = await crypto.subtle.digest('SHA-256', data);
    return Array.from(new Uint8Array(hash))
      .map((b) => b.toString(16).padStart(2, '0'))
      .join('');
  }
  // Fallback: pure JS SHA-256 for non-secure contexts (HTTP over IP)
  return sha256Fallback(message);
}

function sha256Fallback(msg: string): string {
  const K = [
    0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,
    0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,
    0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,
    0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,
    0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,
    0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,
    0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,
    0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2
  ];
  const r = (n: number, x: number) => (x >>> n) | (x << (32 - n));
  const bytes = new TextEncoder().encode(msg);
  const len = bytes.length;
  const pad = new Uint8Array(((len + 9 + 63) & ~63));
  pad.set(bytes); pad[len] = 0x80;
  const dv = new DataView(pad.buffer);
  dv.setUint32(pad.length - 4, len * 8, false);
  let [h0,h1,h2,h3,h4,h5,h6,h7] = [0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19];
  for (let off = 0; off < pad.length; off += 64) {
    const w = new Int32Array(64);
    for (let i = 0; i < 16; i++) w[i] = dv.getInt32(off + i * 4, false);
    for (let i = 16; i < 64; i++) {
      const s0 = r(7,w[i-15]) ^ r(18,w[i-15]) ^ (w[i-15] >>> 3);
      const s1 = r(17,w[i-2]) ^ r(19,w[i-2]) ^ (w[i-2] >>> 10);
      w[i] = (w[i-16] + s0 + w[i-7] + s1) | 0;
    }
    let [a,b,c,d,e,f,g,h] = [h0,h1,h2,h3,h4,h5,h6,h7];
    for (let i = 0; i < 64; i++) {
      const S1 = r(6,e) ^ r(11,e) ^ r(25,e);
      const ch = (e & f) ^ (~e & g);
      const t1 = (h + S1 + ch + K[i] + w[i]) | 0;
      const S0 = r(2,a) ^ r(13,a) ^ r(22,a);
      const maj = (a & b) ^ (a & c) ^ (b & c);
      const t2 = (S0 + maj) | 0;
      h=g; g=f; f=e; e=(d+t1)|0; d=c; c=b; b=a; a=(t1+t2)|0;
    }
    h0=(h0+a)|0; h1=(h1+b)|0; h2=(h2+c)|0; h3=(h3+d)|0;
    h4=(h4+e)|0; h5=(h5+f)|0; h6=(h6+g)|0; h7=(h7+h)|0;
  }
  return [h0,h1,h2,h3,h4,h5,h6,h7].map(v=>(v>>>0).toString(16).padStart(8,'0')).join('');
}
