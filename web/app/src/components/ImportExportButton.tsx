import { useRef, useState, useCallback, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { Button, Modal, Toggle, FormStack, Hint, useToast, useModalState } from '@hy2scale/ui';
import * as XLSX from 'xlsx';
import { IMPORT_TARGETS, type ImportTargetKey } from '@/config/importTargets';

/** Export / Import buttons of a list: .xlsx with the target's columns. */
export default function ImportExportButton({ target }: { target: ImportTargetKey }) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();
  const fileRef = useRef<HTMLInputElement>(null);
  const dlg = useModalState();
  const spec = IMPORT_TARGETS[target];

  const [overwrite, setOverwrite] = useState(false);
  const [processing, setProcessing] = useState(false);
  const [result, setResult] = useState('');

  const handleExport = useCallback(async () => {
    try {
      const ws = XLSX.utils.json_to_sheet(await spec.exportRows(), { header: spec.headers });
      const wb = XLSX.utils.book_new();
      XLSX.utils.book_append_sheet(wb, ws, spec.sheet);
      XLSX.writeFile(wb, `${spec.sheet}.xlsx`);
      toast.success(t('import.exported'));
    } catch (e: any) {
      toast.error(String(e.message || e));
    }
  }, [spec, toast, t]);

  const handleImport = useCallback(async (file: File) => {
    setProcessing(true);
    setResult('');
    try {
      const wb = XLSX.read(await file.arrayBuffer());
      const rows = XLSX.utils.sheet_to_json<any>(wb.Sheets[wb.SheetNames[0]]);
      if (rows.length === 0) {
        toast.error(t('import.empty'));
        return;
      }
      let added = 0, skipped = 0, errors = 0;
      const cache = new Map<string, unknown>();
      for (const row of rows) {
        try {
          if ((await spec.importRow(row, overwrite, cache)) === 'added') added++; else skipped++;
        } catch {
          errors++;
        }
      }
      setResult(t('import.result', { added, skipped, errors }));
      queryClient.invalidateQueries({ queryKey: [spec.queryKey] });
    } catch (e: any) {
      toast.error(t('import.failed') + ': ' + String(e.message || e));
    } finally {
      setProcessing(false);
    }
  }, [spec, overwrite, queryClient, toast, t]);

  const close = () => { dlg.close(); setResult(''); };

  return (
    <>
      <Button size="sm" variant="ghost" onClick={handleExport}>{t('import.export')}</Button>
      <Button size="sm" variant="ghost" onClick={(e: MouseEvent) => dlg.show(undefined, e)}>{t('import.import')}</Button>

      <Modal
        open={dlg.open}
        onClose={close}
        title={t(spec.titleKey)}
        animateFrom={dlg.origin}
        footer={<Button onClick={close}>{t('app.close')}</Button>}
      >
        <FormStack>
          <div
            className="drop-target"
            role="button"
            tabIndex={0}
            onClick={() => fileRef.current?.click()}
            onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); fileRef.current?.click(); } }}
            onDrop={(e) => {
              e.preventDefault();
              const file = e.dataTransfer.files[0];
              if (file) handleImport(file);
            }}
            onDragOver={(e) => e.preventDefault()}
          >
            {processing ? t('import.processing') : t('import.dropHint')}
          </div>

          <input
            ref={fileRef}
            type="file"
            accept=".xlsx"
            hidden
            onChange={(e) => {
              const file = e.target.files?.[0];
              if (file) handleImport(file);
              e.target.value = '';
            }}
          />

          <Toggle size="sm" label={t('import.overwrite')} checked={overwrite} onChange={(e) => setOverwrite(e.target.checked)} />
          <Hint className="hint-small">{t('import.overwriteHint')}</Hint>

          {result && <div className="import-result">{result}</div>}
        </FormStack>
      </Modal>
    </>
  );
}
