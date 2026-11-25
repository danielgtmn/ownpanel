import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import { errorMessage, isNotFound } from '../../shared/api/index.ts'
import { usePageTitle, useResource } from '../../shared/hooks/index.ts'
import { hrefFor, navigate } from '../../shared/routing/index.ts'
import { ArrowLeftIcon, EmptyState, ErrorState, LoadingState, PageHeader, Section } from '../../shared/ui/index.ts'
import { deleteRecord, deleteZone, listRecords, listZones, saveRecord } from './api.ts'
import { DeleteDomainDialog } from './DeleteDomainDialog.tsx'
import { Nameservers } from './Nameservers.tsx'
import { RecordDialog, type RecordSubmit } from './RecordDialog.tsx'
import { fullName, isEditableRecordSet, isProviderManaged, setKey, sortRecordSets, ttlLabel } from './records.ts'
import type { RecordSet, Zone } from './types.ts'
import { findZone, providerLabel } from './zones.ts'

const backLink = (
  <a className="inline-link" href={hrefFor('dns')}>
    <ArrowLeftIcon />
    All domains
  </a>
)

function RecordName({ name, zone }: { name: string; zone: string }) {
  if (name === '@') return <span className="record-name">{zone}</span>
  return (
    <span className="record-name">
      {name}
      <span className="muted">.{zone}</span>
    </span>
  )
}

type RowProps = {
  set: RecordSet
  zone: string
  zoneReadOnly: boolean
  onEdit: (set: RecordSet) => void
  onDelete: (set: RecordSet) => Promise<void>
}

function RecordRow({ set, zone, zoneReadOnly, onEdit, onDelete }: RowProps) {
  const editable = isEditableRecordSet(set, zoneReadOnly)
  const managed = isProviderManaged(set)
  const label = `${set.type} record for ${fullName(set.name, zone)}`
  return (
    <li className="record-row">
      <div className="record-main">
        <h3 className="record-title">
          <RecordName name={set.name} zone={zone} />
          <span className="record-type">{set.type}</span>
        </h3>
        <ul className="record-values" aria-label={`Values of the ${label}`}>
          {set.values.map((value, index) => (
            <li key={`${index}:${value}`}>{value}</li>
          ))}
        </ul>
        <p className="muted record-ttl">TTL: {ttlLabel(set.ttl)}</p>
        {set.locked ? (
          <p className="notice">Locked. This record is protected and can't be changed here.</p>
        ) : null}
      </div>
      {editable ? (
        <div className="record-actions">
          <Button onClick={() => onEdit(set)}>
            Edit<span className="visually-hidden"> the {label}</span>
          </Button>
          <Button data-variant="destructive" pendingAnnouncement="Deleting" onClick={() => onDelete(set)}>
            Delete<span className="visually-hidden"> the {label}</span>
          </Button>
        </div>
      ) : managed ? (
        <p className="muted record-managed">Managed by {providerLabel('hetzner')}</p>
      ) : null}
    </li>
  )
}

type LoadedProps = {
  connectionId: number
  zoneInfo: Zone
  records: RecordSet[]
  reload: () => Promise<void>
}

function Loaded({ connectionId, zoneInfo, records, reload }: LoadedProps) {
  const zone = zoneInfo.name
  const [dialog, setDialog] = useState<{ open: boolean; existing: RecordSet | null }>({ open: false, existing: null })
  const [deleting, setDeleting] = useState(false)
  const editable = !zoneInfo.read_only
  usePageTitle(zone)

  async function removeDomain(confirmName: string) {
    await deleteZone(connectionId, zone, confirmName)
    setDeleting(false)
    toast.success(`${zone} was deleted.`)
    navigate('dns')
  }

  const save: RecordSubmit = async (name, type, body) => {
    await saveRecord(connectionId, zone, name, type, body)
    toast.success(`Saved the ${type} record for ${fullName(name, zone)}. DNS changes can take a few minutes to be visible.`)
    setDialog((current) => ({ ...current, open: false }))
    await reload()
  }

  async function remove(set: RecordSet) {
    const target = fullName(set.name, zone)
    const yes = await confirm({
      title: `Delete the ${set.type} record for ${target}?`,
      description: "It's removed at Hetzner right away. This can't be undone.",
      confirmLabel: 'Delete record',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      await deleteRecord(connectionId, zone, set.name, set.type)
      toast.success(`Deleted the ${set.type} record for ${target}.`)
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await reload().catch(() => undefined)
  }

  const sorted = sortRecordSets(records)

  return (
    <>
      <PageHeader
        back={backLink}
        title={zone}
        meta={<span className="muted">From {zoneInfo.connection_name}</span>}
        actions={
          editable ? (
            <Button data-variant="solid" onClick={() => setDialog({ open: true, existing: null })}>
              Add record
            </Button>
          ) : null
        }
      />
      {zoneInfo.read_only ? (
        <p className="notice notice-spaced">
          This domain is read only at Hetzner, so its records can't be changed here.
        </p>
      ) : null}
      <Section title="Records" description="These records tell the internet where your domain's sites and email live.">
        {sorted.length === 0 ? (
          <EmptyState inSection title="No records yet" description="Add a record to get started." />
        ) : (
          <ul className="record-list" aria-label={`Records of ${zone}`}>
            {sorted.map((set) => (
              <RecordRow
                key={setKey(set)}
                set={set}
                zone={zone}
                zoneReadOnly={zoneInfo.read_only}
                onEdit={(existing) => setDialog({ open: true, existing })}
                onDelete={remove}
              />
            ))}
          </ul>
        )}
      </Section>
      <Section
        title="Nameservers"
        description={`Enter these where you registered ${zone}. The records here only work once the registrar uses them.`}
      >
        <Nameservers nameservers={zoneInfo.nameservers} domain={zone} />
      </Section>
      <Section
        title="Delete domain"
        description="Removes the domain and all its records from the DNS provider."
        tone="danger"
      >
        <Button data-variant="destructive" onClick={() => setDeleting(true)}>
          Delete domain
        </Button>
      </Section>
      <DeleteDomainDialog
        open={deleting}
        onOpenChange={setDeleting}
        zone={zone}
        connectionName={zoneInfo.connection_name}
        onDelete={removeDomain}
      />
      <RecordDialog
        open={dialog.open}
        onOpenChange={(open) => setDialog((current) => ({ ...current, open }))}
        zone={zone}
        existing={dialog.existing}
        onSubmit={save}
      />
    </>
  )
}

export function ZonePage({ connectionId, zone }: { connectionId: number | null; zone: string }) {
  const data = useResource(async () => {
    if (connectionId === null) return null
    const [zones, records] = await Promise.all([listZones(), listRecords(connectionId, zone)])
    const zoneInfo = findZone(zones, connectionId, zone)
    return zoneInfo ? { zoneInfo, records } : null
  }, `dns-zone:${connectionId}:${zone}`)

  if (data.status === 'loading') return <LoadingState label="Loading records…" />
  if (data.status === 'error' && !isNotFound(data.error)) {
    return (
      <>
        {backLink}
        <ErrorState title="Couldn't load the records" message={errorMessage(data.error)} onRetry={data.retry} />
      </>
    )
  }
  if (data.status === 'error' || data.data === null || connectionId === null) {
    return (
      <EmptyState
        title="Domain not found"
        description={`There's no domain "${zone}" in your connections. The connection may have been removed.`}
        action={<a href={hrefFor('dns')}>Back to all domains</a>}
      />
    )
  }
  return <Loaded connectionId={connectionId} zoneInfo={data.data.zoneInfo} records={data.data.records} reload={data.reload} />
}
