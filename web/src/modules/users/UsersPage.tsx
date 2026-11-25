import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { formatDate, isoTimestamp } from '../../shared/format/index.ts'
import { usePageTitle, useResource } from '../../shared/hooks/index.ts'
import { ROLE_LABEL, useSession, type Role, type User } from '../../shared/session/index.ts'
import { ErrorState, LoadingState, PageHeader } from '../../shared/ui/index.ts'
import { createUser, deleteUser, listUsers, updateUser } from './api.ts'
import { AddPersonDialog } from './AddPersonDialog.tsx'
import { NewPasswordDialog } from './NewPasswordDialog.tsx'
import { isSelf, otherRole, roleChangeQuestion } from './rules.ts'

type PersonRowProps = {
  person: User
  self: boolean
  onChangeRole: (person: User) => Promise<void>
  onNewPassword: (person: User) => void
  onRemove: (person: User) => Promise<void>
}

function PersonRow({ person, self, onChangeRole, onNewPassword, onRemove }: PersonRowProps) {
  return (
    <li className="record-row">
      <div className="record-main">
        <h2 className="record-title">
          {person.name}
          {self ? <span className="muted"> (you)</span> : null}
        </h2>
        <ul className="record-facts">
          <li>{ROLE_LABEL[person.role]}</li>
          <li>
            Since <time dateTime={isoTimestamp(person.created_at)}>{formatDate(person.created_at)}</time>
          </li>
        </ul>
        {self ? (
          <p className="muted record-note">Another administrator can change your role or remove you.</p>
        ) : null}
      </div>
      <div className="record-actions">
        {self ? null : (
          <Button pendingAnnouncement="Saving" onClick={() => onChangeRole(person)}>
            {person.role === 'admin' ? 'Make member' : 'Make administrator'}
            <span className="visually-hidden"> ({person.name})</span>
          </Button>
        )}
        <Button onClick={() => onNewPassword(person)}>
          Set new password<span className="visually-hidden"> ({person.name})</span>
        </Button>
        {self ? null : (
          <Button data-variant="destructive" pendingAnnouncement="Removing" onClick={() => onRemove(person)}>
            Remove<span className="visually-hidden"> {person.name}</span>
          </Button>
        )}
      </div>
    </li>
  )
}

export function UsersPage() {
  const { user: me } = useSession()
  const people = useResource(listUsers, 'users')
  const [adding, setAdding] = useState(false)
  const [passwordFor, setPasswordFor] = useState<User | null>(null)
  usePageTitle('People')

  async function add(person: { name: string; password: string; role: Role }) {
    await createUser(person)
    setAdding(false)
    toast.success(`${person.name} was added.`)
    await people.reload()
  }

  async function changeRole(person: User) {
    const question = roleChangeQuestion(person)
    const yes = await confirm({ ...question, cancelLabel: 'Cancel' })
    if (!yes) return
    try {
      await updateUser(person.id, { role: otherRole(person.role) })
      toast.success(`${person.name} is now ${ROLE_LABEL[otherRole(person.role)].toLowerCase()}.`)
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await people.reload()
  }

  async function setPassword(password: string) {
    if (!passwordFor) return
    await updateUser(passwordFor.id, { password })
    toast.success(`The password for ${passwordFor.name} was changed.`)
    setPasswordFor(null)
  }

  async function remove(person: User) {
    const yes = await confirm({
      title: `Remove ${person.name}?`,
      description: `${person.name} can no longer sign in and their access tokens stop working. This can't be undone.`,
      confirmLabel: 'Remove person',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      await deleteUser(person.id)
      toast.success(`${person.name} was removed.`)
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await people.reload()
  }

  return (
    <>
      <PageHeader
        title="People"
        description="Everyone who can sign in to this panel."
        actions={
          <Button data-variant="solid" onClick={() => setAdding(true)}>
            Add person
          </Button>
        }
      />
      {people.status === 'loading' ? <LoadingState label="Loading people…" /> : null}
      {people.status === 'error' ? (
        <ErrorState title="Couldn't load the people" message={errorMessage(people.error)} onRetry={people.retry} />
      ) : null}
      {people.status === 'ready' ? (
        <ul className="record-list" aria-label="People">
          {people.data.map((person) => (
            <PersonRow
              key={person.id}
              person={person}
              self={isSelf(me, person)}
              onChangeRole={changeRole}
              onNewPassword={setPasswordFor}
              onRemove={remove}
            />
          ))}
        </ul>
      ) : null}
      <AddPersonDialog open={adding} onOpenChange={setAdding} onSubmit={add} />
      <NewPasswordDialog name={passwordFor?.name ?? null} onClose={() => setPasswordFor(null)} onSubmit={setPassword} />
    </>
  )
}
