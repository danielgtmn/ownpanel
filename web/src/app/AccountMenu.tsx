import { Menu, MenuContent, MenuItem, MenuTrigger } from '@ui-sdk/react/menu'
import { useState } from 'react'
import { useSession } from '../shared/session/index.ts'
import { ChangePasswordDialog } from './ChangePasswordDialog.tsx'

export function AccountMenu() {
  const { user, signOut } = useSession()
  const [changingPassword, setChangingPassword] = useState(false)

  return (
    <>
      <Menu>
        <MenuTrigger className="account-trigger">
          <span className="account-name">{user.name}</span>
          <span className="visually-hidden"> (account menu)</span>
        </MenuTrigger>
        <MenuContent>
          <MenuItem onSelect={() => setChangingPassword(true)}>Change password</MenuItem>
          <MenuItem onSelect={signOut}>Sign out</MenuItem>
        </MenuContent>
      </Menu>
      <ChangePasswordDialog open={changingPassword} onOpenChange={setChangingPassword} />
    </>
  )
}
