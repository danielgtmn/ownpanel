import { request } from '../../shared/api/index.ts'
import type { Role, User } from '../../shared/session/index.ts'

export function listUsers(): Promise<User[]> {
  return request<User[]>('/users')
}

export function createUser(user: { name: string; email: string; password: string; role: Role }): Promise<User> {
  return request<User>('/users', { method: 'POST', body: user })
}

export function updateUser(id: number, changes: { role?: Role; password?: string; email?: string }): Promise<User> {
  return request<User>(`/users/${id}`, { method: 'PUT', body: changes })
}

export function deleteUser(id: number): Promise<void> {
  return request<void>(`/users/${id}`, { method: 'DELETE' })
}
