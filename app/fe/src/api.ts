import { getAuthSession, useAuthStore } from './stores/auth';

export type ApiEnvelope<T> = {
  msg: string;
  code: number;
  data: T;
};

export type UserProfile = {
  id: string;
  email: string;
  name: string;
  avatar_url?: string;
  plan?: string;
};

export type AuthStartResult = {
  authorize_url: string;
  state: string;
};

export type AuthCallbackResult = {
  user: UserProfile;
  access_token: string;
  refresh_token: string;
  expires_in: number;
};

export type AuthRefreshResult = {
  access_token: string;
  refresh_token: string;
  expires_in: number;
};

export type RuntimeStatus = 'active' | 'expired' | 'unavailable';

export type Project = {
  id: string;
  name: string;
  status: string;
  runtime_status?: RuntimeStatus;
  active_run_id?: string | null;
  last_run_id?: string | null;
  thumbnail_url?: string;
  owner_id?: string;
  last_opened_at?: string;
  created_at?: string;
  updated_at?: string;
  is_shared?: boolean;
};

export type ProjectListResult = {
  items: Project[];
  pagination?: {
    page: number;
    page_size: number;
    total: number;
  };
};

export type ChatMessage = {
  id: string;
  run_id?: string;
  role: 'user' | 'assistant' | 'system' | 'tool';
  content: string;
  status?: 'pending' | 'streaming' | 'completed' | 'failed' | 'cancelled';
  created_at: string;
};

export type ChatHistory = {
  items: ChatMessage[];
  next_cursor?: string | null;
};

export type RunStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled';

export type StartRunResult = {
  run_id: string;
  user_message_id: string;
  status: RunStatus;
};

export type RunDetail = {
  id: string;
  project_id: string;
  status: RunStatus;
  input_message_id?: string;
  assistant_message_id?: string;
  error?: string;
  error_code?: string;
  error_message?: string;
  last_sequence: number;
  created_at: string;
  started_at?: string;
  completed_at?: string;
};

export type RunEventType =
  | 'run.started'
  | 'message.delta'
  | 'tool.started'
  | 'tool.output'
  | 'tool.completed'
  | 'run.completed'
  | 'run.failed'
  | 'run.cancelled'
  | 'ping';

export type RunEvent = {
  type: RunEventType;
  run_id: string;
  conversation_id?: string;
  sequence: number;
  timestamp: string;
  payload: Record<string, unknown>;
};

export type FileTreeNode = {
  path: string;
  name: string;
  type: 'dir' | 'folder' | 'file';
  size?: number;
  mod_time?: string;
  children?: FileTreeNode[];
};

export type FileTreeResult = {
  root: string;
  nodes: FileTreeNode[];
};

export type FileContentResult = {
  path: string;
  size?: number;
  language?: string;
  content: string;
  sha: string;
};

export type FileUpdateResult = {
  path: string;
  size?: number;
  sha: string;
};

export type PreviewResult = {
  preview_id?: string;
  status: 'idle' | 'starting' | 'running' | 'failed' | 'expired';
  preview_url?: string;
  port?: number;
  error?: string;
  last_heartbeat_at?: string;
};

export type PublicationStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled';

export type Publication = {
  id: string;
  project_id: string;
  status: PublicationStatus;
  context: string;
  dockerfile: string;
  image_ref?: string;
  digest?: string;
  logs?: string;
  error_code?: string;
  error_message?: string;
  cancel_requested_at?: string;
  created_at: string;
  started_at?: string;
  completed_at?: string;
};

export type PublicationList = { items: Publication[] };

type RequestOptions = RequestInit & {
  auth?: boolean;
  retryAuth?: boolean;
};

const DEFAULT_BASE_URL = '/api/v1';
const API_BASE_URL = ((import.meta.env.VITE_API_BASE_URL as string | undefined) ?? DEFAULT_BASE_URL).replace(/\/$/, '');
let refreshInFlight: Promise<AuthRefreshResult> | null = null;

export const PROJECT_RUNTIME_EXPIRED_EVENT = 'agentland:project-runtime-expired';

export type ProjectRuntimeExpiredDetail = {
  projectId: string;
};

export class ApiError extends Error {
  status: number;
  code?: string;
  payload?: unknown;

  constructor(message: string, status = 500, payload?: unknown, code?: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.payload = payload;
    this.code = code;
  }
}

function buildUrl(path: string) {
  if (/^https?:\/\//.test(path)) return path;
  return `${API_BASE_URL}${path.startsWith('/') ? path : `/${path}`}`;
}

function errorCode(data: unknown) {
  if (!data || typeof data !== 'object') return undefined;
  const record = data as Record<string, unknown>;
  return typeof record.type === 'string' ? record.type : typeof record.code === 'string' ? record.code : undefined;
}

function projectIdFromPath(path: string) {
  const match = path.match(/\/projects\/([^/?#]+)/);
  if (!match) return undefined;
  try {
    return decodeURIComponent(match[1]);
  } catch {
    return match[1];
  }
}

function reportApiError(error: ApiError, path: string) {
  const projectId = projectIdFromPath(path);
  if (error.code === 'PROJECT_RUNTIME_EXPIRED' && projectId && typeof window !== 'undefined') {
    window.dispatchEvent(new CustomEvent<ProjectRuntimeExpiredDetail>(PROJECT_RUNTIME_EXPIRED_EVENT, {
      detail: { projectId },
    }));
  }
  return error;
}

async function parseEnvelope<T>(response: Response, path = ''): Promise<ApiEnvelope<T>> {
  const text = await response.text();
  let payload: ApiEnvelope<T> | null = null;

  if (text) {
    try {
      payload = JSON.parse(text) as ApiEnvelope<T>;
    } catch {
      throw new ApiError(`Invalid JSON response (${response.status})`, response.status, text);
    }
  }

  if (!response.ok || !payload || payload.code < 200 || payload.code >= 300) {
    throw reportApiError(new ApiError(payload?.msg ?? `HTTP ${response.status}`, response.status, payload?.data ?? text, errorCode(payload?.data)), path);
  }

  return payload;
}

async function refreshAccessToken(): Promise<AuthRefreshResult> {
  if (refreshInFlight) return refreshInFlight;
  const refreshToken = getAuthSession().refreshToken;
  if (!refreshToken) throw new ApiError('Unauthorized', 401, undefined, 'UNAUTHORIZED');

  refreshInFlight = (async () => {
    const response = await fetch(buildUrl('/auth/refresh'), {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: refreshToken }),
    });
    const result = await parseEnvelope<AuthRefreshResult>(response);
    useAuthStore.getState().setTokens(result.data.access_token, result.data.refresh_token);
    return result.data;
  })();

  try {
    return await refreshInFlight;
  } catch (error) {
    useAuthStore.getState().clear();
    throw error;
  } finally {
    refreshInFlight = null;
  }
}

async function rawRequest(path: string, options: RequestOptions = {}): Promise<Response> {
  const { auth = true, retryAuth = true, headers, ...init } = options;
  const requestHeaders = new Headers(headers);
  const accessToken = getAuthSession().accessToken;

  if (auth && accessToken) requestHeaders.set('Authorization', `Bearer ${accessToken}`);
  if (init.body && !(init.body instanceof FormData) && !requestHeaders.has('Content-Type')) {
    requestHeaders.set('Content-Type', 'application/json');
  }

  const response = await fetch(buildUrl(path), { ...init, credentials: 'include', headers: requestHeaders });
  if (auth && retryAuth && response.status === 401 && getAuthSession().refreshToken) {
    await refreshAccessToken();
    return rawRequest(path, { ...options, retryAuth: false });
  }
  return response;
}

export async function apiRequest<T>(path: string, options?: RequestOptions): Promise<T> {
  const response = await rawRequest(path, options);
  const envelope = await parseEnvelope<T>(response, path);
  return envelope.data;
}

export function startGithubAuth(redirectUri: string) {
  return apiRequest<AuthStartResult>('/auth/github/start', {
    method: 'POST',
    auth: false,
    body: JSON.stringify({ redirect_uri: redirectUri }),
  });
}

export function completeGithubAuth(code: string, state: string) {
  return apiRequest<AuthCallbackResult>('/auth/github/callback', {
    method: 'POST',
    auth: false,
    body: JSON.stringify({ code, state }),
  });
}

export function getCurrentUser() {
  return apiRequest<UserProfile>('/auth/me');
}

export function logout(refreshToken: string) {
  return apiRequest<{ success: boolean }>('/auth/logout', {
    method: 'POST',
    body: JSON.stringify({ refresh_token: refreshToken }),
  });
}

export function listProjects(search = '') {
  const query = search ? `?keyword=${encodeURIComponent(search)}` : '';
  return apiRequest<ProjectListResult>(`/projects${query}`);
}

export function getProject(projectId: string) {
  return apiRequest<Project>(`/projects/${encodeURIComponent(projectId)}`);
}

export function createProject(name: string) {
  return apiRequest<Project>('/projects', {
    method: 'POST',
    body: JSON.stringify({ name, template: 'blank' }),
  });
}

export function deleteProject(projectId: string) {
  return apiRequest<{ success: boolean }>(`/projects/${encodeURIComponent(projectId)}`, { method: 'DELETE' });
}

export function listMessages(projectId: string, cursor?: string) {
  const query = cursor ? `?cursor=${encodeURIComponent(cursor)}` : '';
  return apiRequest<ChatHistory>(`/projects/${encodeURIComponent(projectId)}/messages${query}`);
}

export function startRun(projectId: string, message: string, idempotencyKey: string) {
  return apiRequest<StartRunResult>(`/projects/${encodeURIComponent(projectId)}/runs`, {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify({ message }),
  });
}

export function getRun(runId: string) {
  return apiRequest<RunDetail>(`/runs/${encodeURIComponent(runId)}`);
}

export function cancelRun(runId: string) {
  return apiRequest<{ status: RunStatus }>(`/runs/${encodeURIComponent(runId)}/cancel`, { method: 'POST' });
}

export function getFileTree(projectId: string, path = '.') {
  return apiRequest<FileTreeResult>(`/projects/${encodeURIComponent(projectId)}/files/tree?path=${encodeURIComponent(path)}`);
}

export function getFileContent(projectId: string, path: string) {
  return apiRequest<FileContentResult>(`/projects/${encodeURIComponent(projectId)}/files/content?path=${encodeURIComponent(path)}`);
}

export function updateFileContent(projectId: string, path: string, content: string, sha: string) {
  return apiRequest<FileUpdateResult>(`/projects/${encodeURIComponent(projectId)}/files/content?path=${encodeURIComponent(path)}`, {
    method: 'PUT',
    body: JSON.stringify({ content, sha }),
  });
}

export function getPreview(projectId: string) {
  return apiRequest<PreviewResult>(`/projects/${encodeURIComponent(projectId)}/preview`);
}

export function startPreview(projectId: string, port: number) {
  return apiRequest<PreviewResult>(`/projects/${encodeURIComponent(projectId)}/previews`, {
    method: 'POST',
    body: JSON.stringify({ port }),
  });
}

export function createPublication(projectId: string, buildContext: string, dockerfile: string, idempotencyKey: string) {
  return apiRequest<Publication>(`/projects/${encodeURIComponent(projectId)}/publications`, {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify({ context: buildContext, dockerfile }),
  });
}

export function listPublications(projectId: string) {
  return apiRequest<PublicationList>(`/projects/${encodeURIComponent(projectId)}/publications`);
}

export function getPublication(publicationId: string) {
  return apiRequest<Publication>(`/publications/${encodeURIComponent(publicationId)}`);
}

export function cancelPublication(publicationId: string) {
  return apiRequest<{ id: string; status: PublicationStatus }>(`/publications/${encodeURIComponent(publicationId)}/cancel`, { method: 'POST' });
}

type StreamRunOptions = {
  signal: AbortSignal;
  lastEventId?: string;
  onEvent: (event: RunEvent) => void;
  onLastEventId?: (id: string) => void;
};

function parseSseFrame(frame: string) {
  let eventName = '';
  let id = '';
  const data: string[] = [];

  for (const line of frame.split('\n')) {
    if (!line || line.startsWith(':')) continue;
    const separator = line.indexOf(':');
    const field = separator === -1 ? line : line.slice(0, separator);
    const value = separator === -1 ? '' : line.slice(separator + 1).replace(/^ /, '');
    if (field === 'event') eventName = value;
    if (field === 'id') id = value;
    if (field === 'data') data.push(value);
  }

  return { eventName, id, data: data.join('\n') };
}

async function consumeEventStream(
  response: Response,
  signal: AbortSignal,
  handleFrame: (frame: ReturnType<typeof parseSseFrame>) => void,
) {
  if (!response.body) throw new ApiError('SSE response has no body', response.status);
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  while (!signal.aborted) {
    const { value, done } = await reader.read();
    buffer += decoder.decode(value, { stream: !done }).replace(/\r\n/g, '\n');
    let boundary = buffer.indexOf('\n\n');
    while (boundary >= 0) {
      const frame = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      if (frame) handleFrame(parseSseFrame(frame));
      boundary = buffer.indexOf('\n\n');
    }
    if (done) break;
  }
}

export async function streamRunEvents(runId: string, options: StreamRunOptions) {
  const headers = new Headers({ Accept: 'text/event-stream' });
  if (options.lastEventId) headers.set('Last-Event-ID', options.lastEventId);
  const response = await rawRequest(`/runs/${encodeURIComponent(runId)}/events`, {
    headers,
    signal: options.signal,
  });
  if (!response.ok) {
    await parseEnvelope<never>(response, `/runs/${encodeURIComponent(runId)}/events`);
    throw new ApiError(`SSE HTTP ${response.status}`, response.status);
  }

  await consumeEventStream(response, options.signal, ({ eventName, id, data }) => {
    if (!data) return;
    const event = JSON.parse(data) as RunEvent;
    if (eventName && event.type !== eventName) event.type = eventName as RunEventType;
    const nextId = id || String(event.sequence);
    if (nextId) options.onLastEventId?.(nextId);
    options.onEvent(event);
  });
}
