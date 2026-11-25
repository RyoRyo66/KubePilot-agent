export interface Workspace {
  id: string; ownerId: string; name: string; description: string;
  createdAt: string; onboardingComplete: boolean;
}
export interface Session {
  authenticated: boolean; registrationOpen: boolean;
  account?: { id: string; username: string }; workspace?: Workspace | null;
}
export interface ActiveSession extends Session {
  account: { id: string; username: string }; workspace: Workspace;
}
export class APIError extends Error {
  constructor(message: string, public status: number, public code?: string) { super(message) }
}
export async function accountAPI(path: string, body?: unknown, identity?: { accountId: string; workspaceId?: string }) {
  const response = await fetch('/api' + path, {
    credentials: 'same-origin', cache: 'no-store',
    method: body === undefined ? 'GET' : 'POST',
    headers: {
      ...(body === undefined ? {} : { 'Content-Type': 'application/json', 'X-Requested-With': 'KubePilot' }),
      ...(identity ? { 'X-KubePilot-Account': identity.accountId } : {}),
      ...(identity?.workspaceId ? { 'X-KubePilot-Workspace': identity.workspaceId } : {}),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  })
  const result = await response.json().catch(() => ({}))
  if (!response.ok) throw new APIError(result.error || '服务暂时不可用，请稍后重试', response.status, result.code)
  return result
}
