import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Switch, SwitchThumb } from '@ui-sdk/react/switch'
import { TimeInput } from '@ui-sdk/react/time-input'
import { toast } from '@ui-sdk/react/toast'
import { useEffect, useState } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { formatDate, formatDateTime } from '../../shared/format/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { startPolling } from '../../shared/settings/index.ts'
import { ExternalIcon, LoadingState, Section, StatusBadge } from '../../shared/ui/index.ts'
import {
  CHANNELS,
  channelHint,
  channelName,
  isBusy,
  needsReload,
  POLL_INTERVAL,
  progressText,
  progressTitle,
  resultNotice,
  splitWindow,
  updateQuestion,
  windowProblem,
} from './updates.ts'
import { getUpdates, requestUpdate, setAutoUpdate, type Channel, type UpdateInfo } from './updatesApi.ts'

const TITLE = 'Updates'
const DESCRIPTION = 'New versions of the panel and the web server that serves your sites.'

// The version this page was loaded with: the web app comes with the panel, so after an update it reloads.
let loadedVersion: string | null = null

function Progress({ info, unreachable }: { info: UpdateInfo; unreachable: boolean }) {
  return (
    <div className="update-progress" role="status" aria-live="polite">
      <span className="spinner" aria-hidden="true" />
      <div className="choice-text">
        <strong>{progressTitle(info.status)}</strong>
        <span className="muted">{progressText(info.status, unreachable)}</span>
      </div>
    </div>
  )
}

function LastResult({ info }: { info: UpdateInfo }) {
  const notice = resultNotice(info.status)
  if (!notice) return null
  return (
    <div className="update-result" data-tone={notice.tone}>
      <StatusBadge tone={notice.tone}>{notice.title}</StatusBadge>
      {notice.detail ? <p>{notice.detail}</p> : null}
      {info.status.finished_at ? <p className="muted">{formatDateTime(info.status.finished_at)}</p> : null}
    </div>
  )
}

type ChannelRowProps = {
  info: UpdateInfo
  channel: Channel
  busy: boolean
  onInstall: (channel: Channel) => void
}

function ChannelRow({ info, channel, busy, onInstall }: ChannelRowProps) {
  const release = info.available[channel]
  const current = info.installed.channel === channel
  const upToDate = current && release?.version === info.installed.version
  const disabled = busy || !info.supported

  let action = null
  if (current && info.update_available && release) {
    action = (
      <Button data-variant="solid" disabled={disabled} onClick={() => onInstall(channel)}>
        Update to {release.version}
      </Button>
    )
  } else if (upToDate) {
    action = <StatusBadge tone="positive">Up to date</StatusBadge>
  } else if (!current && release) {
    action = (
      <Button disabled={disabled} onClick={() => onInstall(channel)}>
        Switch to this channel
      </Button>
    )
  }

  return (
    <li className="update-channel" data-current={current ? '' : undefined}>
      <div className="update-channel-main">
        <div className="update-channel-name">
          <strong>{channelName(channel)}</strong>
          {current ? <StatusBadge tone="neutral">Installed</StatusBadge> : null}
        </div>
        <span className="muted">{channelHint(channel)}</span>
        {release ? (
          <span className="update-channel-release">
            Newest: <strong>{release.version}</strong>, {formatDate(release.published_at)}
            {release.notes_url ? (
              <>
                {' · '}
                <a className="inline-link" href={release.notes_url} target="_blank" rel="noreferrer noopener">
                  What's new
                  <ExternalIcon />
                </a>
              </>
            ) : null}
            {release.older && !current ? <span className="muted"> (older than the installed version)</span> : null}
          </span>
        ) : (
          <span className="muted">{info.check_failed ? 'Unknown right now.' : 'Nothing released yet.'}</span>
        )}
      </div>
      {action ? <div className="update-channel-action">{action}</div> : null}
    </li>
  )
}

function AutoUpdatesForm({ info, onSaved }: { info: UpdateInfo; onSaved: (info: UpdateInfo) => void }) {
  const channel = info.installed.channel
  const [initialFrom, initialTo] = splitWindow(info.auto.window)
  const [enabled, setEnabled] = useState(info.auto.mode !== 'off')
  const [from, setFrom] = useState<string | null>(initialFrom)
  const [to, setTo] = useState<string | null>(initialTo)
  const [error, setError] = useState<string | null>(null)
  const unavailable = !info.supported || channel === ''
  const unchanged = enabled === (info.auto.mode !== 'off') && `${from}-${to}` === info.auto.window

  async function save() {
    const problem = enabled ? windowProblem(from, to) : null
    if (problem) {
      setError(problem)
      return
    }
    try {
      const next = await setAutoUpdate({
        mode: enabled && channel ? channel : 'off',
        window: from && to ? `${from}-${to}` : info.auto.window,
      })
      onSaved(next)
      toast.success(next.auto.mode === 'off' ? 'Automatic updates are off.' : 'Automatic updates are on.')
    } catch (caught) {
      setError(errorMessage(caught))
    }
  }

  return (
    <div className="update-auto">
      <h3>Automatic updates</h3>
      <Field className="update-switch" disabled={unavailable}>
        <Switch
          checked={enabled}
          onCheckedChange={(next) => {
            setEnabled(next === true)
            setError(null)
          }}
        >
          <SwitchThumb />
        </Switch>
        <div className="choice-text">
          <FieldLabel>Install new versions automatically</FieldLabel>
          <FieldDescription>
            Once a day within the hours below (server time), a newer version from {channelName(channel).toLowerCase()} is
            installed. If it doesn't start, the previous version is put back.
          </FieldDescription>
        </div>
      </Field>
      {enabled ? (
        <div className="update-window">
          <Field invalid={error ? true : undefined} disabled={unavailable}>
            <FieldLabel>From</FieldLabel>
            <TimeInput hourCycle={24} value={from} onValueChange={(value) => { setFrom(value); setError(null) }} />
          </Field>
          <Field invalid={error ? true : undefined} disabled={unavailable}>
            <FieldLabel>Until</FieldLabel>
            <TimeInput hourCycle={24} value={to} onValueChange={(value) => { setTo(value); setError(null) }} />
          </Field>
        </div>
      ) : null}
      {error ? (
        <Field invalid>
          <FieldError match>{error}</FieldError>
        </Field>
      ) : null}
      <div className="settings-actions">
        <Button pendingAnnouncement="Saving" disabled={unavailable || unchanged} onClick={save}>
          Save
        </Button>
      </div>
    </div>
  )
}

function Loaded({ initial, onRetryCheck }: { initial: UpdateInfo; onRetryCheck: () => void }) {
  const [info, setInfo] = useState(initial)
  const [watching, setWatching] = useState(() => isBusy(initial.status))
  const [unreachable, setUnreachable] = useState(false)

  useEffect(() => {
    if (loadedVersion === null) loadedVersion = initial.installed.version
  }, [initial])

  useEffect(() => {
    if (!watching) return
    return startPolling({
      load: (signal) => getUpdates(signal),
      interval: POLL_INTERVAL,
      onResult: (next) => {
        setUnreachable(false)
        setInfo(next)
        if (needsReload(loadedVersion, next)) {
          window.location.reload()
          return
        }
        if (!isBusy(next.status)) setWatching(false)
      },
      // While the panel restarts, requests fail; keep asking.
      onError: () => setUnreachable(true),
      isDone: (next) => !isBusy(next.status),
    })
  }, [watching])

  async function install(channel: Channel) {
    const release = info.available[channel]
    const yes = await confirm({ ...updateQuestion(info, channel), cancelLabel: 'Cancel' })
    if (!yes) return
    try {
      const next = await requestUpdate({ channel, allow_downgrade: release?.older === true })
      setInfo(next)
      setWatching(true)
    } catch (caught) {
      toast.error(errorMessage(caught))
    }
  }

  const busy = watching || isBusy(info.status)

  return (
    <Section title={TITLE} description={DESCRIPTION}>
      <div className="settings-stack">
        <p className="update-installed">
          Installed: <strong>{info.installed.version}</strong>
          {info.installed.channel ? <span className="muted"> · {channelName(info.installed.channel)}</span> : null}
        </p>
        {!info.supported ? (
          <p className="notice">
            This panel wasn't set up with the installer, so it can't update itself. Update it the same way you installed
            it.
          </p>
        ) : null}
        {busy ? <Progress info={info} unreachable={unreachable} /> : <LastResult info={info} />}
        {info.check_failed ? (
          <div className="notice update-check-failed">
            <span>Couldn't check for new versions right now. Try again in a few minutes.</span>
            <Button onClick={onRetryCheck}>Check again</Button>
          </div>
        ) : null}
        <ul className="update-channels" aria-label="Release channels">
          {CHANNELS.map((channel) => (
            <ChannelRow key={channel} info={info} channel={channel} busy={busy} onInstall={install} />
          ))}
        </ul>
        <AutoUpdatesForm key={`${info.auto.mode}|${info.auto.window}`} info={info} onSaved={setInfo} />
      </div>
    </Section>
  )
}

export function UpdatesSection() {
  const updates = useResource(() => getUpdates(), 'updates')
  if (updates.status === 'loading') {
    return (
      <Section title={TITLE} description={DESCRIPTION}>
        <LoadingState label="Looking for new versions…" />
      </Section>
    )
  }
  if (updates.status === 'error') {
    return (
      <Section title={TITLE} description={DESCRIPTION}>
        <div className="notice update-check-failed">
          <span>{errorMessage(updates.error)}</span>
          <Button onClick={updates.retry}>Try again</Button>
        </div>
      </Section>
    )
  }
  return <Loaded initial={updates.data} onRetryCheck={updates.retry} />
}
