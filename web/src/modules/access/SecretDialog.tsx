import { Button } from '@ui-sdk/react/button'
import { Dialog, DialogBackdrop, DialogClose, DialogContent, DialogDescription, DialogTitle } from '@ui-sdk/react/dialog'
import { Field, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { toast } from '@ui-sdk/react/toast'

type SecretDialogProps = {
  name: string
  secret: string | null
  onClose: () => void
}

export function SecretDialog({ name, secret, onClose }: SecretDialogProps) {
  async function copy() {
    if (!secret) return
    try {
      await navigator.clipboard.writeText(secret)
      toast.success('Token copied.')
    } catch {
      toast.error("Couldn't copy. Select the token and copy it yourself.")
    }
  }

  return (
    <Dialog
      open={secret !== null}
      onOpenChange={(next, details) => {
        if (next) return
        if (details.reason === 'outside-press') {
          details.cancel()
          return
        }
        onClose()
      }}
    >
      <DialogBackdrop />
      <DialogContent>
        <div className="dialog-head">
          <DialogTitle>Your new token</DialogTitle>
          <DialogDescription>
            Copy the token for "{name}" now and keep it somewhere safe. It will not be shown again. If you lose it,
            delete the token and create a new one.
          </DialogDescription>
        </div>
        <div className="form">
          <Field name="secret">
            <FieldLabel>Access token</FieldLabel>
            <Input readOnly value={secret ?? ''} spellCheck={false} autoComplete="off" onFocus={(event) => event.currentTarget.select()} />
          </Field>
          <div className="form-actions">
            <Button onClick={copy}>Copy token</Button>
            <DialogClose data-variant="solid">I've saved it</DialogClose>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
