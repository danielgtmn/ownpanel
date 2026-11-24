import { ApiError, errorMessage } from '../api/index.ts'

export function signInErrorMessage(error: unknown): string {
  if (error instanceof ApiError && error.status === 401) {
    return "That name and password don't match. Check them and try again."
  }
  return errorMessage(error)
}
