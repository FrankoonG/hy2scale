import { useCallback, useState } from 'react';

/** Reads `localStorage[key]`; null when missing or storage is blocked. */
export function readStored(key: string): string | null {
  try { return localStorage.getItem(key); } catch { return null; }
}

/** Writes (or with null removes) `localStorage[key]`; ignores blocked storage. */
export function writeStored(key: string, value: string | null) {
  try {
    if (value === null) localStorage.removeItem(key);
    else localStorage.setItem(key, value);
  } catch { /* storage unavailable */ }
}

export interface StoredCodec<T> {
  parse: (raw: string) => T | undefined;
  serialize: (value: T) => string;
}

/** '1' / '0' booleans. */
export const boolCodec: StoredCodec<boolean> = {
  parse: (s) => (s === '1' ? true : s === '0' ? false : undefined),
  serialize: (v) => (v ? '1' : '0'),
};

const jsonCodec: StoredCodec<any> = {
  parse: (s) => { try { return JSON.parse(s); } catch { return undefined; } },
  serialize: (v) => JSON.stringify(v),
};

/**
 * useState persisted in localStorage under `key`. Reads and writes never
 * throw (private mode, blocked storage); an unparsable value falls back to
 * `initial`. Default codec is JSON.
 */
export function useStoredState<T>(
  key: string,
  initial: T,
  codec: StoredCodec<T> = jsonCodec,
): [T, (v: T | ((prev: T) => T)) => void] {
  const [value, setValue] = useState<T>(() => {
    const raw = readStored(key);
    if (raw === null) return initial;
    const v = codec.parse(raw);
    return v === undefined ? initial : v;
  });
  const set = useCallback((v: T | ((prev: T) => T)) => {
    setValue((prev) => {
      const next = typeof v === 'function' ? (v as (p: T) => T)(prev) : v;
      writeStored(key, codec.serialize(next));
      return next;
    });
  }, [key, codec]);
  return [value, set];
}
