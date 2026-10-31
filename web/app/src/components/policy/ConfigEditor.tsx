// Config-mode editor (lazy chunk, CodeMirror YAML): highlighting,
// completion from yamlSchema (Enter or Tab accepts; Tab indents otherwise),
// parse errors linted live, and diagnostics pushed by the caller (server
// validation, a refused tab switch) until the next edit.
import { useEffect, useRef } from 'react';
import { EditorState } from '@codemirror/state';
import { EditorView, keymap, lineNumbers, highlightActiveLine, drawSelection } from '@codemirror/view';
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
import { yaml as yamlLang } from '@codemirror/lang-yaml';
import { HighlightStyle, syntaxHighlighting, indentOnInput, bracketMatching } from '@codemirror/language';
import { acceptCompletion, autocompletion, completionKeymap, type CompletionContext } from '@codemirror/autocomplete';
import { linter, lintGutter, setDiagnostics, type Diagnostic } from '@codemirror/lint';
import { tags } from '@lezer/highlight';
import { lintConfig } from './setConfig';
import { suggest, type YamlData } from './yamlSchema';

export interface ConfigEditorProps {
  value: string;
  onChange: (v: string) => void;
  data: YamlData;
  /** Diagnostics from a failed save (offsets into value); cleared on edit. */
  extra: { from: number; to: number; message: string }[];
}

const highlight = HighlightStyle.define([
  { tag: [tags.propertyName, tags.definition(tags.propertyName)], color: 'var(--primary)' },
  { tag: [tags.string, tags.special(tags.string)], color: 'var(--c-green)' },
  { tag: [tags.number, tags.bool, tags.null, tags.atom, tags.keyword], color: 'var(--c-orange)' },
  { tag: [tags.comment, tags.lineComment], color: 'var(--text-muted)', fontStyle: 'italic' },
  { tag: [tags.punctuation, tags.separator, tags.squareBracket, tags.brace], color: 'var(--text-secondary)' },
]);

const theme = EditorView.theme({
  '&': { fontSize: '13px', backgroundColor: 'var(--surface)', color: 'var(--text)', height: '100%' },
  '.cm-content': { fontFamily: 'var(--mono)', caretColor: 'var(--text)' },
  '.cm-gutters': { backgroundColor: 'var(--bg)', color: 'var(--text-muted)', border: 'none' },
  '.cm-activeLine': { backgroundColor: 'var(--surface-hover)' },
  '.cm-activeLineGutter': { backgroundColor: 'var(--surface-hover)' },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': { overflow: 'auto' },
  '.cm-tooltip': { backgroundColor: 'var(--surface)', border: '1px solid var(--border)', color: 'var(--text)' },
  '.cm-tooltip-autocomplete > ul > li[aria-selected]': { backgroundColor: 'var(--primary-light)', color: 'var(--primary)' },
  '.cm-completionDetail': { color: 'var(--text-muted)', marginLeft: '8px' },
});

export default function ConfigEditor({ value, onChange, data, extra }: ConfigEditorProps) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const dataRef = useRef(data);
  const extraRef = useRef(extra);
  const changeRef = useRef(onChange);
  const lintRef = useRef<(v: EditorView) => Diagnostic[]>(() => []);
  dataRef.current = data;
  changeRef.current = onChange;

  useEffect(() => {
    const complete = (ctx: CompletionContext) => {
      const line = ctx.state.doc.lineAt(ctx.pos);
      const res = suggest(ctx.state.doc.toString(), line.number - 1, ctx.pos - line.from, dataRef.current);
      if (!res || (!ctx.explicit && res.from === ctx.pos - line.from && !/[\s:-]$/.test(line.text.slice(0, ctx.pos - line.from)))) return null;
      return {
        from: line.from + res.from,
        options: res.options.map((o) => ({ label: o.label, detail: o.detail, type: o.type })),
        validFor: /^[\w./-]*$/,
      };
    };
    const lint = (v: EditorView): Diagnostic[] => {
      const text = v.state.doc.toString();
      const out: Diagnostic[] = lintConfig(text).map((d) => ({ from: d.from, to: d.to, severity: d.error ? 'error' : 'warning', message: d.message }));
      for (const x of extraRef.current) {
        out.push({ from: Math.min(x.from, text.length), to: Math.min(Math.max(x.to, x.from), text.length), severity: 'error', message: x.message });
      }
      return out;
    };
    lintRef.current = lint;
    view.current = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(), history(), drawSelection(), highlightActiveLine(), indentOnInput(), bracketMatching(),
          yamlLang(), syntaxHighlighting(highlight), theme, EditorState.tabSize.of(2),
          autocompletion({ override: [complete], activateOnTyping: true }),
          linter(lint, { delay: 300 }), lintGutter(),
          // Tab: accept an open completion, else indent.
          keymap.of([{ key: 'Tab', run: acceptCompletion }, ...completionKeymap, ...defaultKeymap, ...historyKeymap, indentWithTab]),
          EditorView.updateListener.of((u) => {
            if (!u.docChanged) return;
            extraRef.current = [];
            changeRef.current(u.state.doc.toString());
          }),
        ],
      }),
    });
    return () => view.current?.destroy();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // External value changes (load) replace the document.
  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== value) v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } });
  }, [value]);

  useEffect(() => {
    extraRef.current = extra;
    // The linter only reruns after an edit: push the combined set now.
    const v = view.current;
    if (v) v.dispatch(setDiagnostics(v.state, lintRef.current(v)));
  }, [extra]);

  return <div ref={host} className="config-editor" data-testid="config-editor" />;
}
