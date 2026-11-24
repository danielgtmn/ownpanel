import { Button } from '@ui-sdk/react/button'

type ErrorStateProps = {
  title?: string
  message: string
  onRetry?: () => void
}

export function ErrorState({ title = "Couldn't load this page", message, onRetry }: ErrorStateProps) {
  return (
    <div className="empty-state" data-tone="danger">
      <h2>{title}</h2>
      <p className="muted">{message}</p>
      {onRetry ? <Button onClick={onRetry}>Try again</Button> : null}
    </div>
  )
}
