import { Button } from '@ui-sdk/react/button'
import { copyText } from '../../shared/ui/index.ts'
import { nameserverList } from './zones.ts'

export function Nameservers({ nameservers, domain }: { nameservers: string[]; domain: string }) {
  const list = nameserverList(nameservers)
  if (list.length === 0) {
    return <p className="muted">The DNS provider didn't report any nameservers for {domain}.</p>
  }
  return (
    <ul className="nameserver-list" aria-label={`Nameservers of ${domain}`}>
      {list.map((name) => (
        <li key={name}>
          <code>{name}</code>
          <Button onClick={() => copyText(name)}>
            Copy<span className="visually-hidden"> {name}</span>
          </Button>
        </li>
      ))}
    </ul>
  )
}
