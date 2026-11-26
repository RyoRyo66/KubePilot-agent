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
  const timeout = AbortSignal.timeout(path === '/platform/setup/model' ? 60000 : 15000)
  try {
  const response = await fetch('/api' + path, {
    signal: timeout, credentials: 'same-origin', cache: 'no-store',
    method: body === undefined ? 'GET' : 'POST',
    headers: {
      ...(body === undefined ? {} : { 'Content-Type': 'application/json', 'X-Requested-With': 'KubePilot' }),
      ...(identity ? { 'X-KubePilot-Account': identity.accountId } : {}),
      ...(identity?.workspaceId ? { 'X-KubePilot-Workspace': identity.workspaceId } : {}),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  })
  const result = await response.json().catch(() => { throw new APIError('服务返回了无效的数据，请检查平台连接', response.status) })
  if (!response.ok) throw new APIError(result.error || '服务暂时不可用，请稍后重试', response.status, result.code)
  return result
  } catch (error) {
    if (timeout.aborted) throw new APIError('请求超时，请刷新并核对操作结果', 408)
    throw error
  }
}
