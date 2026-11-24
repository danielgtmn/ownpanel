import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { formatDateTime, isoTimestamp } from '../../shared/format/index.ts'
import { usePageTitle, useResource } from '../../shared/hooks/index.ts'
import { EmptyState, ErrorState, LoadingState, PageHeader } from '../../shared/ui/index.ts'
import { createToken, deleteToken, listTokens } from './api.ts'
import { accessLabel, lastUsedLabel, scopeLabel } from './rules.ts'
import { SecretDialog } from './SecretDialog.tsx'
import { TokenFormDialog } from './TokenFormDialog.tsx'
import type { NewToken, Token } from './types.ts'

function TokenRow({ token, onDelete }: { token: Token; onDelete: (token: Token) => Promise<void> }) {
  return (
    <li className="record-row">
      <div className="record-main">
        <h2 className="record-title">{token.name}</h2>
        <ul className="record-facts">
          <li>{accessLabel(token.access)}</li>
          <li>{scopeLabel(token.sites)}</li>
          <li>
            Created <time dateTime={isoTimestamp(token.created_at)}>{formatDateTime(token.created_at)}</time>
          </li>
          <li>{lastUsedLabel(token)}</li>
        </ul>
      </div>
      <div className="record-actions">
        <Button data-variant="destructive" pendingAnnouncement="Deleting" onClick={() => onDelete(token)}>
          Delete<span className="visually-hidden"> token {token.name}</span>
        </Button>
      </div>
    </li>
  )
}

export function AccessPage() {
  const tokens = useResource(listTokens, 'tokens')
  const [creating, setCreating] = useState(false)
  const [secret, setSecret] = useState<string | null>(null)
  const [secretName, setSecretName] = useState('')
  usePageTitle('Access tokens')

  async function create(token: NewToken) {
    const result = await createToken(token)
    setCreating(false)
    setSecretName(result.name)
    setSecret(result.token)
    await tokens.reload()
  }

  async function remove(token: Token) {
    const yes = await confirm({
      title: `Delete ${token.name}?`,
      description: "Programs that use this token stop working right away. This can't be undone.",
      confirmLabel: 'Delete token',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      await deleteToken(token.id)
      toast.success(`${token.name} was deleted.`)
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await tokens.reload()
  }

  const newTokenButton = (
    <Button data-variant="solid" onClick={() => setCreating(true)}>
      New token
    </Button>
  )

  return (
    <>
      <PageHeader
        title="Access tokens"
        description="Tokens let programs and scripts act on your behalf without your password."
        actions={tokens.status === 'ready' && tokens.data.length > 0 ? newTokenButton : null}
      />
      {tokens.status === 'loading' ? <LoadingState label="Loading your tokens…" /> : null}
      {tokens.status === 'error' ? (
        <ErrorState title="Couldn't load your tokens" message={errorMessage(tokens.error)} onRetry={tokens.retry} />
      ) : null}
      {tokens.status === 'ready' && tokens.data.length === 0 ? (
        <EmptyState
          title="No access tokens yet"
          description="Create a token when a program or script needs to work with your sites."
          action={newTokenButton}
        />
      ) : null}
      {tokens.status === 'ready' && tokens.data.length > 0 ? (
        <ul className="record-list" aria-label="Your access tokens">
          {tokens.data.map((token) => (
            <TokenRow key={token.id} token={token} onDelete={remove} />
          ))}
        </ul>
      ) : null}
      <TokenFormDialog open={creating} onOpenChange={setCreating} onSubmit={create} />
      <SecretDialog name={secretName} secret={secret} onClose={() => setSecret(null)} />
    </>
  )
}
