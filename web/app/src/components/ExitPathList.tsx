import { useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Autocomplete, FormGroup } from '@hy2scale/ui';
import { useExitPaths } from '@/hooks/useExitPaths';
import clsx from 'clsx';

export interface ExitPathValue {
  paths: string[];
  mode: '' | 'quality' | 'aggregate';
}

interface ExitPathListProps {
  value: ExitPathValue;
  onChange: (val: ExitPathValue) => void;
  label?: string;
}

export function ExitPathList({ value, onChange, label }: ExitPathListProps) {
  const { t } = useTranslation();
  const { exitPaths } = useExitPaths();

  const paths = value.paths.length > 0 ? value.paths : [''];
  const mode = value.mode;

  const updatePath = useCallback((index: number, val: string) => {
    const newPaths = [...paths];
    newPaths[index] = val;
    onChange({ paths: newPaths, mode: value.mode });
  }, [paths, value.mode, onChange]);

  const addPath = useCallback(() => {
    onChange({ paths: [...paths, ''], mode: paths.length === 0 ? '' : (value.mode || 'quality') });
  }, [paths, value.mode, onChange]);

  const removePath = useCallback((index: number) => {
    const newPaths = paths.filter((_, i) => i !== index);
    const newMode = newPaths.length <= 1 ? '' : value.mode;
    onChange({ paths: newPaths, mode: newMode });
  }, [paths, value.mode, onChange]);

  const setMode = useCallback((m: '' | 'quality' | 'aggregate') => {
    onChange({ paths: value.paths, mode: m });
  }, [value.paths, onChange]);

  const hasMultiple = paths.filter(Boolean).length > 1;
  const singleOnly = !hasMultiple && paths.filter(Boolean).length <= 1;

  return (
    <FormGroup label={label || t('users.exitVia')}>
      {/* Mode selection — styled pills matching old exit-mode-opt */}
      <div className={clsx('exit-mode-options', singleOnly && !hasMultiple && 'exit-mode-disabled')}>
        <label className="exit-mode-opt">
          <input
            type="radio"
            name="exitMode"
            checked={mode === '' || !mode}
            onChange={() => setMode('')}
            disabled={hasMultiple}
          />
          {t('exit.modeNone')}
        </label>
        <label className="exit-mode-opt">
          <input
            type="radio"
            name="exitMode"
            checked={mode === 'quality'}
            onChange={() => setMode('quality')}
            disabled={singleOnly}
          />
          {t('exit.modeStability')}
        </label>
        <label className="exit-mode-opt">
          <input
            type="radio"
            name="exitMode"
            checked={mode === 'aggregate'}
            onChange={() => setMode('aggregate')}
            disabled={singleOnly}
          />
          {t('exit.modeSpeed')}
        </label>
      </div>

      {/* Path inputs */}
      <div className="addr-list">
        {paths.map((p, i) => (
          <div key={i} className="addr-row">
            <Autocomplete
              options={exitPaths}
              value={p}
              onChange={(v) => updatePath(i, v)}
              placeholder={t('users.exitViaHint')}
            />
            {paths.length > 1 && (
              <button
                type="button"
                className="addr-del"
                onClick={() => removePath(i)}
                title={t('app.delete')}
              >
                ×
              </button>
            )}
          </div>
        ))}
        <div className="addr-add-row" onClick={addPath}>
          {t('exit.addPath')}
        </div>
      </div>
    </FormGroup>
  );
}

/** Extract exit_via (first path) and exit_paths/exit_mode for API */
export function exitPathToApi(val: ExitPathValue) {
  const filtered = val.paths.filter(Boolean);
  return {
    exit_via: filtered[0] || '',
    exit_paths: filtered.length > 1 ? filtered : undefined,
    exit_mode: val.mode || undefined,
  };
}

/** Parse API response into ExitPathValue */
export function apiToExitPath(exitVia?: string, exitPaths?: string[], exitMode?: string): ExitPathValue {
  const paths = exitPaths && exitPaths.length > 0 ? exitPaths : (exitVia ? [exitVia] : ['']);
  return { paths, mode: (exitMode as ExitPathValue['mode']) || '' };
}
