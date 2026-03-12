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

export type Project = {
  id: string;
  name: string;
  status: string;
  thumbnail_url?: string;
  owner_id?: string;
  last_opened_at?: string;
  created_at?: string;
  updated_at?: string;
  is_shared?: boolean;
  metadata?: {
    last_view_mode?: 'preview' | 'code';
  };
};

export type ProjectUpdateResult = {
  id: string;
  name?: string;
  updated_at?: string;
  metadata?: {
    last_view_mode?: 'preview' | 'code';
  };
};

export type GenerationAttachment = {
  file_id: string;
  name: string;
};

export type GenerationJob = {
  job_id: string;
  status: string;
};

export type ProjectListResult = {
  items: Project[];
  pagination?: {
    page: number;
    page_size: number;
    total: number;
  };
};

export type ProjectUsage = {
  used: number;
  limit: number;
};

export type JobDetail = {
  job_id: string;
  type?: string;
  status: string;
  progress?: number;
  logs?: string[];
  result?: unknown;
};

export type ChatMessage = {
  id: string;
  role: 'user' | 'assistant' | 'system';
  content: string;
  created_at?: string;
};

export type ChatHistory = {
  conversation_id: string;
  items: ChatMessage[];
  next_cursor?: string | null;
};

export type ChatConversation = {
  id: string;
  title: string;
  updated_at?: string;
};

export type ChatConversationList = {
  items: ChatConversation[];
};

export type ChatSendResult = {
  user_message?: ChatMessage;
  assistant_message?: ChatMessage;
  changes?: Array<{ path: string; action: string }>;
};

type ChatDeltaEvent = ApiEnvelope<{
  text: string;
}>;

type ChatDoneEvent = ApiEnvelope<{
  message_id?: string;
  changes?: Array<{ path: string; action: string }>;
}>;

type SendChatMessageOptions = {
  onDelta?: (fullText: string, deltaText: string) => void;
};

export type FileTreeNode = {
  path: string;
  name: string;
  type: 'folder' | 'file';
  size?: number;
  children?: FileTreeNode[];
};

export type FileTreeResult = {
  root: string;
  nodes: FileTreeNode[];
};

export type FileContentResult = {
  path: string;
  language?: string;
  content: string;
  sha?: string;
};

export type FileDownloadResult = {
  file_name: string;
};

export type FileUploadResult = {
  file_id: string;
  name: string;
  size: number;
  mime_type: string;
  download_url?: string;
};

export type PreviewResult = {
  preview_id?: string;
  status?: string;
  preview_url?: string;
  last_heartbeat_at?: string;
};

const DEFAULT_BASE_URL = '/api/v1';

function normalizeBaseUrl() {
  const raw = (import.meta.env.VITE_API_BASE_URL as string | undefined) ?? DEFAULT_BASE_URL;
  return raw.endsWith('/') ? raw.slice(0, -1) : raw;
}

const API_BASE_URL = normalizeBaseUrl();

export class ApiError extends Error {
  status: number;
  payload?: unknown;

  constructor(message: string, status = 500, payload?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.payload = payload;
  }
}

function buildUrl(path: string) {
  if (/^https?:\/\//.test(path)) {
    return path;
  }
  const normalizedPath = path.startsWith('/') ? path : `/${path}`;
  return `${API_BASE_URL}${normalizedPath}`;
}

function buildHeaders(initHeaders: HeadersInit | undefined, accessToken?: string, jsonBody = true) {
  const headers = new Headers(initHeaders);
  if (jsonBody && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }
  if (accessToken && !headers.has('Authorization')) {
    headers.set('Authorization', `Bearer ${accessToken}`);
  }
  return headers;
}

async function request<T>(
  path: string,
  init: RequestInit = {},
  accessToken?: string,
): Promise<T> {
  const isFormData = typeof FormData !== 'undefined' && init.body instanceof FormData;
  const response = await fetch(buildUrl(path), {
    ...init,
    headers: buildHeaders(init.headers, accessToken, init.body !== undefined && !isFormData),
  });

  const text = await response.text();
  let payload: ApiEnvelope<T> | null = null;

  if (text) {
    try {
      payload = JSON.parse(text) as ApiEnvelope<T>;
    } catch (error) {
      throw new ApiError(
        `Invalid JSON response from ${path}: ${(error as Error).message}`,
        response.status,
        text,
      );
    }
  }

  if (!response.ok) {
    throw new ApiError(payload?.msg ?? `HTTP ${response.status}`, response.status, payload);
  }

  if (!payload) {
    throw new ApiError(`Empty response from ${path}`, response.status);
  }

  if (payload.code !== 200) {
    throw new ApiError(payload.msg || 'Business error', response.status, payload);
  }

  return payload.data;
}

export function sleep(ms: number) {
  return new Promise<void>((resolve) => setTimeout(resolve, ms));
}

export async function startGithubAuth(redirectUri: string): Promise<AuthStartResult> {
  return request<AuthStartResult>('/auth/github/start', {
    method: 'POST',
    body: JSON.stringify({ redirect_uri: redirectUri }),
  });
}

export async function completeGithubAuth(code: string, state: string): Promise<AuthCallbackResult> {
  return request<AuthCallbackResult>('/auth/github/callback', {
    method: 'POST',
    body: JSON.stringify({ code, state }),
  });
}

export async function refreshAuthToken(refreshToken: string): Promise<AuthRefreshResult> {
  return request<AuthRefreshResult>('/auth/refresh', {
    method: 'POST',
    body: JSON.stringify({ refresh_token: refreshToken }),
  });
}

export async function getCurrentUser(accessToken?: string): Promise<UserProfile> {
  return request<UserProfile>('/auth/me', { method: 'GET' }, accessToken);
}

export async function logout(refreshToken: string, accessToken?: string): Promise<{ success: boolean }> {
  return request<{ success: boolean }>('/auth/logout', {
    method: 'POST',
    body: JSON.stringify({ refresh_token: refreshToken }),
  }, accessToken);
}

export async function createProject(
  input: { name: string; template?: string },
  accessToken?: string,
): Promise<Project> {
  return request<Project>('/projects', {
    method: 'POST',
    body: JSON.stringify({
      name: input.name,
      template: input.template ?? 'blank',
    }),
  }, accessToken);
}

export async function listProjects(
  input: {
    view?: 'all' | 'recent' | 'shared';
    keyword?: string;
    status?: string;
    sort_by?: string;
    sort_order?: 'asc' | 'desc';
    page?: number;
    page_size?: number;
  } = {},
  accessToken?: string,
): Promise<ProjectListResult> {
  const params = new URLSearchParams();
  if (input.view) params.set('view', input.view);
  if (input.keyword) params.set('keyword', input.keyword);
  if (input.status) params.set('status', input.status);
  if (input.sort_by) params.set('sort_by', input.sort_by);
  if (input.sort_order) params.set('sort_order', input.sort_order);
  if (input.page) params.set('page', String(input.page));
  if (input.page_size) params.set('page_size', String(input.page_size));

  const query = params.toString();
  return request<ProjectListResult>(`/projects${query ? `?${query}` : ''}`, { method: 'GET' }, accessToken);
}

export async function getProject(projectId: string, accessToken?: string): Promise<Project> {
  return request<Project>(`/projects/${encodeURIComponent(projectId)}`, { method: 'GET' }, accessToken);
}

export async function updateProject(
  projectId: string,
  input: { name?: string; metadata?: { last_view_mode?: 'preview' | 'code' } },
  accessToken?: string,
): Promise<ProjectUpdateResult> {
  return request<ProjectUpdateResult>(`/projects/${encodeURIComponent(projectId)}`, {
    method: 'PATCH',
    body: JSON.stringify(input),
  }, accessToken);
}

export async function deleteProject(projectId: string, accessToken?: string): Promise<{ success: boolean }> {
  return request<{ success: boolean }>(`/projects/${encodeURIComponent(projectId)}`, { method: 'DELETE' }, accessToken);
}

export async function getProjectUsage(accessToken?: string): Promise<ProjectUsage> {
  return request<ProjectUsage>('/projects/usage', { method: 'GET' }, accessToken);
}

export async function createGeneration(
  projectId: string,
  prompt: string,
  attachments: GenerationAttachment[] = [],
  accessToken?: string,
): Promise<GenerationJob> {
  return request<GenerationJob>(`/projects/${encodeURIComponent(projectId)}/generations`, {
    method: 'POST',
    body: JSON.stringify({
      prompt,
      attachments,
    }),
  }, accessToken);
}

export async function getJob(jobId: string, accessToken?: string): Promise<JobDetail> {
  return request<JobDetail>(`/jobs/${encodeURIComponent(jobId)}`, { method: 'GET' }, accessToken);
}

export async function getChatConversations(
  projectId: string,
  accessToken?: string,
): Promise<ChatConversationList> {
  return request<ChatConversationList>(`/projects/${encodeURIComponent(projectId)}/chat/conversations`, { method: 'GET' }, accessToken);
}

export async function getChatMessages(
  projectId: string,
  conversationId: string,
  cursor = '',
  accessToken?: string,
): Promise<ChatHistory> {
  const params = new URLSearchParams({
    conversation_id: conversationId,
    cursor,
  });
  return request<ChatHistory>(
    `/projects/${encodeURIComponent(projectId)}/chat/messages?${params.toString()}`,
    { method: 'GET' },
    accessToken,
  );
}

export async function sendChatMessage(
  projectId: string,
  conversationId: string,
  content: string,
  accessToken?: string,
  options: SendChatMessageOptions = {},
): Promise<ChatSendResult> {
  const response = await fetch(buildUrl(`/projects/${encodeURIComponent(projectId)}/chat/messages`), {
    method: 'POST',
    headers: buildHeaders(
      {
        Accept: 'text/event-stream',
      },
      accessToken,
      true,
    ),
    body: JSON.stringify({
      conversation_id: conversationId,
      content,
      attachments: [],
    }),
  });

  if (!response.ok) {
    const text = await response.text();
    const contentType = response.headers.get('Content-Type') ?? '';

    if (contentType.includes('application/json') && text) {
      try {
        const payload = JSON.parse(text) as ApiEnvelope<unknown>;
        throw new ApiError(payload.msg ?? `HTTP ${response.status}`, response.status, payload);
      } catch (error) {
        if (error instanceof ApiError) {
          throw error;
        }
      }
    }

    throw new ApiError(text || `HTTP ${response.status}`, response.status, text);
  }

  if (!response.body) {
    throw new ApiError('Missing SSE response body.', response.status);
  }

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  let assistantText = '';
  let messageId = '';
  let changes: Array<{ path: string; action: string }> = [];

  const processEvent = (rawEvent: string) => {
    const data = rawEvent
      .split('\n')
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice(5).trim())
      .join('\n');

    if (!data) {
      return;
    }

    const payload = JSON.parse(data) as ChatDeltaEvent | ChatDoneEvent;
    if (payload.code !== 200) {
      throw new ApiError(payload.msg || 'Business error', response.status, payload);
    }

    if (payload.msg === 'delta') {
      const deltaText = (payload.data as { text?: string }).text ?? '';
      if (!deltaText) {
        return;
      }
      assistantText += deltaText;
      options.onDelta?.(assistantText, deltaText);
      return;
    }

    if (payload.msg === 'done') {
      const doneData = payload.data as { message_id?: string; changes?: Array<{ path: string; action: string }> };
      messageId = doneData.message_id ?? messageId;
      changes = doneData.changes ?? changes;
      return;
    }

    if (payload.msg === 'error') {
      throw new ApiError('Chat stream failed.', response.status, payload);
    }
  };

  while (true) {
    const { value, done } = await reader.read();
    buffer += decoder.decode(value ?? new Uint8Array(), { stream: !done }).replace(/\r/g, '');

    let boundaryIndex = buffer.indexOf('\n\n');
    while (boundaryIndex !== -1) {
      const eventBlock = buffer.slice(0, boundaryIndex).trim();
      buffer = buffer.slice(boundaryIndex + 2);
      if (eventBlock) {
        processEvent(eventBlock);
      }
      boundaryIndex = buffer.indexOf('\n\n');
    }

    if (done) {
      break;
    }
  }

  const tail = buffer.trim();
  if (tail) {
    processEvent(tail);
  }

  return {
    assistant_message: {
      id: messageId || `m_assistant_${Date.now()}`,
      role: 'assistant',
      content: assistantText,
      created_at: new Date().toISOString(),
    },
    changes,
  };
}

export async function getFileTree(
  projectId: string,
  path = '/workspace',
  depth = 3,
  accessToken?: string,
): Promise<FileTreeResult> {
  const params = new URLSearchParams({
    path,
    depth: String(depth),
  });
  return request<FileTreeResult>(
    `/projects/${encodeURIComponent(projectId)}/files/tree?${params.toString()}`,
    { method: 'GET' },
    accessToken,
  );
}

export async function getFileContent(
  projectId: string,
  path: string,
  accessToken?: string,
): Promise<FileContentResult> {
  const params = new URLSearchParams({ path });
  return request<FileContentResult>(
    `/projects/${encodeURIComponent(projectId)}/files/content?${params.toString()}`,
    { method: 'GET' },
    accessToken,
  );
}

function parseDownloadFileName(contentDisposition: string | null, fallbackFileName: string) {
  if (!contentDisposition) {
    return fallbackFileName;
  }

  const utf8Match = contentDisposition.match(/filename\*\s*=\s*UTF-8''([^;]+)/i);
  if (utf8Match?.[1]) {
    try {
      return decodeURIComponent(utf8Match[1]);
    } catch {
      return utf8Match[1];
    }
  }

  const plainMatch = contentDisposition.match(/filename\s*=\s*"?([^";]+)"?/i);
  if (plainMatch?.[1]) {
    return plainMatch[1];
  }

  return fallbackFileName;
}

function triggerDownload(blob: Blob, fileName: string) {
  const downloadUrl = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = downloadUrl;
  anchor.download = fileName;
  anchor.style.display = 'none';
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(downloadUrl), 60_000);
}

async function download(path: string, init: RequestInit = {}, accessToken?: string, fallbackFileName = 'download.bin') {
  const response = await fetch(buildUrl(path), {
    ...init,
    headers: buildHeaders(init.headers, accessToken, false),
  });

  if (!response.ok) {
    const text = await response.text();
    const contentType = response.headers.get('Content-Type') ?? '';

    if (contentType.includes('application/json') && text) {
      try {
        const payload = JSON.parse(text) as ApiEnvelope<unknown>;
        throw new ApiError(payload.msg ?? `HTTP ${response.status}`, response.status, payload);
      } catch (error) {
        if (error instanceof ApiError) {
          throw error;
        }
      }
    }

    throw new ApiError(text || `HTTP ${response.status}`, response.status, text);
  }

  const blob = await response.blob();
  const fileName = parseDownloadFileName(response.headers.get('Content-Disposition'), fallbackFileName);
  triggerDownload(blob, fileName);
  return { file_name: fileName } satisfies FileDownloadResult;
}

export async function downloadProject(
  projectId: string,
  projectName?: string,
  accessToken?: string,
): Promise<FileDownloadResult> {
  const archiveBaseName = (projectName ?? projectId)
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, '-')
    .replace(/^-+|-+$/g, '') || projectId;

  return download(
    `/projects/${encodeURIComponent(projectId)}/download`,
    { method: 'GET' },
    accessToken,
    `${archiveBaseName}.zip`,
  );
}

export async function uploadImageAttachment(file: File, accessToken?: string): Promise<FileUploadResult> {
  if (!file.type.startsWith('image/')) {
    throw new ApiError('Only image attachments are supported.', 400);
  }

  const formData = new FormData();
  formData.append('file', file);
  formData.append('purpose', 'generation');

  return request<FileUploadResult>('/files', {
    method: 'POST',
    body: formData,
  }, accessToken);
}

export async function getFileMeta(fileId: string, accessToken?: string): Promise<FileUploadResult> {
  return request<FileUploadResult>(`/files/${encodeURIComponent(fileId)}`, { method: 'GET' }, accessToken);
}

export async function startPreview(projectId: string, accessToken?: string): Promise<PreviewResult> {
  return request<PreviewResult>(`/projects/${encodeURIComponent(projectId)}/preview/start`, {
    method: 'POST',
    body: JSON.stringify({
      device: 'desktop',
      port: 3000,
    }),
  }, accessToken);
}

export async function getPreview(projectId: string, accessToken?: string): Promise<PreviewResult> {
  return request<PreviewResult>(`/projects/${encodeURIComponent(projectId)}/preview`, {
    method: 'GET',
  }, accessToken);
}
