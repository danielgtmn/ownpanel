import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { useState } from 'react'
import { FormDialog } from '../ui/index.ts'
import { ServerAddresses } from './ServerAddresses.tsx'

/** A button that explains, in a dialog, how to point domains that aren't managed here to this server. */
export function PointHelp({ domains }: { domains: string[] }) {
  const [open, setOpen] = useState(false)
  const one = domains.length === 1 ? domains[0] : null
  return (
    <>
      <Button onClick={() => setOpen(true)}>
        {one ? 'How to point it here' : 'How to point them here'}
        <span className="visually-hidden"> for {domains.join(', ')}</span>
      </Button>
      <FormDialog
        open={open}
        onOpenChange={setOpen}
        title={one ? `Point ${one} here` : 'Point your domains here'}
        description={`${one ?? domains.join(', ')} ${one ? "isn't" : "aren't"} managed from here. Connect the company that manages ${one ? 'it' : 'them'} under DNS, or set an A or AAAA record to these addresses yourself.`}
      >
        <div className="form">
          <ServerAddresses />
          <div className="form-actions">
            <DialogClose>Close</DialogClose>
          </div>
        </div>
      </FormDialog>
    </>
  )
}
