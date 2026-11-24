import { Dialog, DialogBackdrop, DialogContent, DialogDescription, DialogTitle } from '@ui-sdk/react/dialog'
import { useState, type ReactNode } from 'react'

type FormDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: string
  children: ReactNode
}

export function FormDialog({ open, onOpenChange, title, description, children }: FormDialogProps) {
  const [session, setSession] = useState(0)
  const [wasOpen, setWasOpen] = useState(open)
  if (open !== wasOpen) {
    setWasOpen(open)
    if (open) setSession((count) => count + 1)
  }

  return (
    <Dialog open={open} onOpenChange={(next) => onOpenChange(next)}>
      <DialogBackdrop />
      <DialogContent>
        <div className="dialog-head">
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </div>
        <div key={session}>{children}</div>
      </DialogContent>
    </Dialog>
  )
}
