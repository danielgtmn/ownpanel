import { Button } from '@ui-sdk/react/button'
import { errorMessage, getServerAddresses } from '../api/index.ts'
import { useResource } from '../hooks/index.ts'
import { copyText } from '../ui/index.ts'

export function ServerAddresses() {
  const addresses = useResource(getServerAddresses, 'server-addresses')
  if (addresses.status === 'loading') return <p className="muted">Looking up this server's addresses…</p>
  if (addresses.status === 'error') {
    return (
      <p className="muted">
        Couldn't look up this server's addresses. {errorMessage(addresses.error)}{' '}
        <Button onClick={addresses.retry}>Try again</Button>
      </p>
    )
  }
  if (addresses.data.length === 0) {
    return (
      <p className="notice">
        This server's public address isn't set up yet. Ask an administrator to set it before you point a domain here.
      </p>
    )
  }
  return (
    <ul className="address-list" aria-label="This server's addresses">
      {addresses.data.map((ip) => (
        <li key={ip}>
          <code>{ip}</code>
          <Button onClick={() => copyText(ip)}>
            Copy<span className="visually-hidden"> {ip}</span>
          </Button>
        </li>
      ))}
    </ul>
  )
}
