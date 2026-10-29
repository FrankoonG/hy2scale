import { type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Card, Button, Badge, Table, useSelection, useModalState, type Column } from '@hy2scale/ui';
import ResponsiveActions from '@/components/ResponsiveActions';
import CertModal from '@/components/CertModal';
import { BulkButtons, EditSelectedButton, pickSelected } from '@/components/SelectionActions';
import { useBulkAction } from '@/hooks/useBulkAction';
import { ListScope } from '@/hooks/useDeselectOnBlankClick';
import { isPast } from '@/utils/date';
import * as api from '@/api';
import type { CertInfo } from '@/api';
import { boldMarkup } from '@/utils/richText';

export default function TLSPage() {
  const { t } = useTranslation();
  const { data: certs = [] } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts });

  const edit = useModalState<CertInfo | null>();
  const selection = useSelection(certs.map((c) => c.id));
  const selected = pickSelected(certs, selection.selected, (c) => c.id);
  const bulk = useBulkAction(['certs']);
  const bulkDelete = () => bulk(selection.selected, (id) => api.deleteCert(id), {
    label: t('app.bulkDelete'),
    confirm: { title: t('app.bulkDelete'), message: t('app.bulkDeleteConfirm', { count: selection.count }), confirmText: t('app.delete'), danger: true },
  });

  const certColumns: Column<CertInfo>[] = [
    {
      key: 'name', title: t('tls.name'), sortValue: (cert) => cert.name || cert.id, render: (cert) => (
        <>
          <b>{cert.name || cert.id}</b>
          <span className="peer-addr-sub">{cert.id}</span>
          {isPast(cert.not_after) && <> <Badge variant="muted">{t('tls.expired')}</Badge></>}
        </>
      ),
    },
    { key: 'subject', title: t('tls.subject'), sortValue: (cert) => cert.subject || null, render: (cert) => cert.subject },
    { key: 'issuer', title: t('tls.issuer'), sortValue: (cert) => cert.issuer || null, render: (cert) => <>{cert.issuer}{cert.is_ca && <> <Badge variant="blue">CA</Badge></>}</> },
    {
      key: 'expires', title: t('tls.expires'),
      sortValue: (cert) => { const ts = Date.parse(cert.not_after); return Number.isNaN(ts) ? null : ts; },
      render: (cert) => <span className="mono">{cert.not_after}</span>,
    },
    { key: 'key', title: t('tls.hasKey'), sortValue: (cert) => cert.key_file ? 1 : 0, render: (cert) => cert.key_file ? <Badge variant="green">{t('app.yes')}</Badge> : <Badge variant="muted">{t('app.no')}</Badge> },
  ];

  return (
    <div className="hy-page">
      <Card
        fill={1}
        title={t('tls.title')}
        count={certs.length}
        actions={
          <ResponsiveActions selectedCount={selection.count} onClearSelection={selection.clear}>
            <BulkButtons selected={selected} onDelete={bulkDelete} />
            <EditSelectedButton selected={selected} onEdit={(c, e) => edit.show(c, e)} />
            <Button size="sm" variant="primary" onClick={(e: MouseEvent) => edit.show(null, e)}>{t('tls.new')}</Button>
          </ResponsiveActions>
        }
        noPadding
      >
        <ListScope selection={selection}>
          <Table
            id="tls"
            columns={certColumns}
            data={certs}
            rowKey={(c) => c.id}
            rowClassName={(c) => isPast(c.not_after) ? 'disabled-row' : undefined}
            emptyText={boldMarkup(t('tls.noCerts'))}
            selection={selection}
          />
        </ListScope>
      </Card>

      <CertModal {...edit.modalProps} editing={edit.data ?? null} />
    </div>
  );
}
