import { useEffect } from 'react'

// The account area on the mail server's name, where mailbox users sign in
// to manage their own mailbox. Only the frame for now; signing in and the
// account pages follow (wiki/Groupware-Architecture.md, G1).
export function AccountScreen({ host }: { host: string }) {
  useEffect(() => {
    document.title = `Sign in to your mailbox · ${host}`
  }, [host])

  return (
    <div className="gate">
      <main className="gate-card">
        <div className="gate-head">
          <span className="brand">{host}</span>
          <h1>Sign in to your mailbox</h1>
          <p>
            Here you'll manage your own mailbox: change your password and create passwords for your mail programs and
            phone.
          </p>
          <p className="notice" role="status">
            Signing in here isn't available yet. Until then, ask whoever set up your mailbox.
          </p>
        </div>
      </main>
    </div>
  )
}
