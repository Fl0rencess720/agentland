import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { motion } from 'motion/react';
import {
  HelpCircle,
  Bot,
  Send,
  Eye,
  Code2,
  Monitor,
  Tablet,
  Smartphone,
  ArrowLeft,
  Folder,
  Loader2,
  AlertCircle,
  Rocket,
} from 'lucide-react';
import CodeEditor from './CodeEditor';
import { useI18n } from '../i18n';
import DeepToggle from './DeepToggle';
import LanguageSwitcher from './LanguageSwitcher';
import UserMenu from './UserMenu';
import {
  ApiError,
  getChatMessages,
  getFileContent,
  downloadProject,
  getFileTree,
  getJob,
  getPreview,
  sendChatMessage,
  sleep,
  startPreview,
  updateProject,
  type ChatMessage,
  type FileTreeNode,
  type UserProfile,
} from '../api';

type WorkspaceProps = {
  onBack: () => void;
  onProjects: () => void;
  onLogout: () => void;
  currentUser: UserProfile | null;
  projectId: string;
  projectName: string;
  initialPrompt: string;
  initialViewMode?: 'preview' | 'code';
  generationJobId?: string;
  accessToken?: string;
  deepEnabled: boolean;
  onDeepEnabledChange: (next: boolean) => void;
};

const PREVIEW_POLL_INTERVAL_MS = 1500;
const PREVIEW_POLL_MAX_ATTEMPTS = 20;
const GENERATION_POLL_INTERVAL_MS = 1200;
const CODE_REFRESH_INTERVAL_MS = 10000;
const TERMINAL_GENERATION_STATUSES = new Set(['SUCCESS', 'FAILED', 'CANCELED']);

function extractAssistantText(result: unknown) {
  if (!result || typeof result !== 'object') return '';
  const content = (result as { assistant_text?: unknown }).assistant_text;
  return typeof content === 'string' ? content : '';
}

function messageTime(input?: string) {
  if (!input) return 'just now';
  const date = new Date(input);
  if (Number.isNaN(date.getTime())) return 'just now';
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function messageOrderValue(role: ChatMessage['role']) {
  switch (role) {
    case 'user':
      return 1;
    case 'assistant':
      return 2;
    default:
      return 3;
  }
}

function sortChatMessages(messages: ChatMessage[]) {
  return [...messages].sort((left, right) => {
    const leftTime = left.created_at ? new Date(left.created_at).getTime() : 0;
    const rightTime = right.created_at ? new Date(right.created_at).getTime() : 0;
    if (leftTime !== rightTime) {
      return leftTime - rightTime;
    }
    return messageOrderValue(left.role) - messageOrderValue(right.role);
  });
}

function shouldKeepPreviewLoading(error: unknown) {
  if (!(error instanceof ApiError)) {
    return false;
  }
  return error.status === 400 || error.status === 409 || error.message === 'invalid_argument' || error.message === 'runtime_unavailable';
}

export default function Workspace({
  onBack,
  onProjects,
  onLogout,
  currentUser,
  projectId,
  projectName,
  initialPrompt,
  initialViewMode = 'preview',
  generationJobId,
  accessToken,
  deepEnabled,
  onDeepEnabledChange,
}: WorkspaceProps) {
  const [viewMode, setViewMode] = useState<'preview' | 'code'>(initialViewMode);
  const { t } = useI18n();

  const [reloadKey, setReloadKey] = useState(0);

  const [bootstrapLoading, setBootstrapLoading] = useState(true);
  const [bootstrapError, setBootstrapError] = useState<string | null>(null);

  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [pendingGenerationMessages, setPendingGenerationMessages] = useState<ChatMessage[]>([]);
  const [chatInput, setChatInput] = useState('');
  const [chatSubmitting, setChatSubmitting] = useState(false);
  const [chatError, setChatError] = useState<string | null>(null);

  const [fileTree, setFileTree] = useState<FileTreeNode[]>([]);
  const [fileLoading, setFileLoading] = useState(true);
  const [fileError, setFileError] = useState<string | null>(null);

  const [previewId, setPreviewId] = useState<string | null>(null);
  const [previewStatus, setPreviewStatus] = useState<string>('IDLE');
  const [previewUrl, setPreviewUrl] = useState<string | null>(null);
  const [previewLoading, setPreviewLoading] = useState(true);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [isGenerationOutputting, setIsGenerationOutputting] = useState(Boolean(generationJobId));
  const [isChatOutputting, setIsChatOutputting] = useState(false);
  const [codeRefreshSignal, setCodeRefreshSignal] = useState(0);
  const fileRefreshInFlightRef = useRef(false);
  const previewUrlRef = useRef<string | null>(null);

  const isAgentOutputting = isGenerationOutputting || isChatOutputting;

  useEffect(() => {
    previewUrlRef.current = previewUrl;
  }, [previewUrl]);

  const refreshFileTree = useCallback(async (isCanceled: () => boolean, options?: { showLoading?: boolean; markRefresh?: boolean }) => {
    if (fileRefreshInFlightRef.current) {
      return;
    }

    fileRefreshInFlightRef.current = true;
    const showLoading = options?.showLoading ?? false;

    if (showLoading || fileTree.length === 0) {
      setFileLoading(true);
    }

    try {
      const treeData = await getFileTree(projectId, '/workspace', 3, accessToken);
      if (isCanceled()) return;

      setFileTree(treeData.nodes ?? []);
      setFileError(null);
      if (options?.markRefresh) {
        setCodeRefreshSignal((previous) => previous + 1);
      }
    } catch (error) {
      if (isCanceled()) return;
      setFileError((error as Error).message || 'Failed to load workspace files.');
    } finally {
      fileRefreshInFlightRef.current = false;
      if (!isCanceled()) {
        setFileLoading(false);
      }
    }
  }, [accessToken, fileTree.length, projectId]);

  const waitForPreviewReady = useCallback(async (isCanceled: () => boolean) => {
    const previewData = await startPreview(projectId, accessToken);

    if (isCanceled()) return;

    const initialStatus = previewData.status ?? 'STARTING';
    setPreviewId(previewData.preview_id ?? null);
    setPreviewStatus(initialStatus);
    setPreviewUrl(previewData.preview_url ?? null);

    if (!previewData.preview_id || initialStatus === 'RUNNING') {
      setPreviewLoading(false);
      return;
    }

    for (let attempt = 0; attempt < PREVIEW_POLL_MAX_ATTEMPTS; attempt += 1) {
      if (isCanceled()) return;

      await sleep(PREVIEW_POLL_INTERVAL_MS);
      const status = await getPreview(projectId, accessToken);

      if (isCanceled()) return;

      setPreviewStatus(status.status ?? 'RUNNING');
      if (status.preview_url) {
        setPreviewUrl(status.preview_url);
      }

      if (status.status === 'RUNNING') {
        setPreviewLoading(false);
        return;
      }
    }

    throw new Error('Preview startup timeout.');
  }, [accessToken, projectId]);

  const refreshPreview = useCallback(async (isCanceled: () => boolean, options?: { showLoading?: boolean }) => {
    const showLoading = options?.showLoading ?? false;

    if (showLoading || !previewUrlRef.current) {
      setPreviewLoading(true);
    }
    setPreviewError(null);

    try {
      await waitForPreviewReady(isCanceled);
    } catch (error) {
      if (isCanceled()) return;
      if (shouldKeepPreviewLoading(error)) {
        setPreviewStatus('STARTING');
        setPreviewError(null);
        setPreviewLoading(true);
        return;
      }
      setPreviewError((error as Error).message || 'Failed to load workspace preview.');
      setPreviewLoading(false);
    }
  }, [waitForPreviewReady]);

  const loadWorkspace = useCallback(async (isCanceled: () => boolean) => {
    setBootstrapLoading(true);
    setBootstrapError(null);
    setChatError(null);

    try {
      const chatData = await getChatMessages(projectId, '', accessToken);

      if (isCanceled()) return;

      setMessages(chatData.items ?? []);
    } catch (error) {
      if (isCanceled()) return;

      const message = (error as Error).message || 'Failed to load workspace.';
      setBootstrapError(message);
      return;
    } finally {
      if (!isCanceled()) {
        setBootstrapLoading(false);
      }
    }

    await refreshFileTree(isCanceled, { showLoading: true });
    if (isCanceled()) return;

    await refreshPreview(isCanceled, { showLoading: true });
  }, [accessToken, projectId, refreshFileTree, refreshPreview]);

  const finalizeWorkspaceAfterGeneration = useCallback(async (isCanceled: () => boolean) => {
    try {
      const chatData = await getChatMessages(projectId, '', accessToken);
      if (isCanceled()) return;
      setMessages(chatData.items ?? []);
    } catch (error) {
      if (!isCanceled()) {
        setChatError((error as Error).message || 'Failed to sync generation.');
      }
    }

    if (isCanceled()) return;
    await refreshFileTree(isCanceled, { showLoading: false, markRefresh: true });
    if (isCanceled()) return;
    await refreshPreview(isCanceled, { showLoading: true });
  }, [accessToken, projectId, refreshFileTree, refreshPreview]);

  useEffect(() => {
    setIsGenerationOutputting(Boolean(generationJobId));
  }, [generationJobId, projectId]);

  useEffect(() => {
    let canceled = false;
    void loadWorkspace(() => canceled);
    return () => {
      canceled = true;
    };
  }, [loadWorkspace, reloadKey]);

  useEffect(() => {
    if (!generationJobId || !initialPrompt.trim()) {
      setPendingGenerationMessages([]);
      return;
    }

    const now = new Date().toISOString();
    setPendingGenerationMessages([
      {
        id: `generation_user_${generationJobId}`,
        role: 'user',
        content: initialPrompt.trim(),
        created_at: now,
      },
      {
        id: `generation_assistant_${generationJobId}`,
        role: 'assistant',
        content: '',
        created_at: now,
      },
    ]);
  }, [generationJobId, initialPrompt, projectId]);

  useEffect(() => {
    if (!generationJobId) {
      return;
    }

    let canceled = false;
    let refreshedAfterCompletion = false;

    const syncGeneration = async () => {
      while (!canceled) {
        try {
          const job = await getJob(generationJobId, accessToken);
          const assistantText = extractAssistantText(job.result).trim();

          setPendingGenerationMessages((previous) => {
            const now = new Date().toISOString();
            const base = previous.length > 0
              ? previous
              : initialPrompt.trim()
                ? [
                    {
                      id: `generation_user_${generationJobId}`,
                      role: 'user' as const,
                      content: initialPrompt.trim(),
                      created_at: now,
                    },
                    {
                      id: `generation_assistant_${generationJobId}`,
                      role: 'assistant' as const,
                      content: '',
                      created_at: now,
                    },
                  ]
                : [];

            const assistantIndex = base.findIndex((message) => message.id === `generation_assistant_${generationJobId}`);
            const next = [...base];
            const assistantMessage = {
              id: `generation_assistant_${generationJobId}`,
              role: 'assistant' as const,
              content: assistantText,
              created_at: next[0]?.created_at ?? now,
            };

            if (assistantIndex >= 0) {
              next[assistantIndex] = {
                ...next[assistantIndex],
                content: assistantText,
              };
            } else {
              next.push(assistantMessage);
            }

            return next;
          });

          if (job.status === 'FAILED' || job.status === 'CANCELED') {
            if (!canceled) {
              setIsGenerationOutputting(false);
              setPendingGenerationMessages([]);
              setChatError(`Generation ${job.status.toLowerCase()}`);
              setPreviewStatus(job.status);
            }
            return;
          }

          if (job.status === 'SUCCESS') {
            if (!refreshedAfterCompletion) {
              refreshedAfterCompletion = true;
              await finalizeWorkspaceAfterGeneration(() => canceled);
              if (canceled) {
                return;
              }
              setIsGenerationOutputting(false);
              setPendingGenerationMessages([]);
            }
            return;
          }

          if (TERMINAL_GENERATION_STATUSES.has(job.status)) {
            return;
          }

          await sleep(GENERATION_POLL_INTERVAL_MS);
        } catch (error) {
          if (!canceled) {
            setChatError((error as Error).message || 'Failed to sync generation.');
          }
          return;
        }
      }
    };

    void syncGeneration();

    return () => {
      canceled = true;
    };
  }, [accessToken, finalizeWorkspaceAfterGeneration, generationJobId, initialPrompt]);

  useEffect(() => {
    if (!isAgentOutputting) {
      return;
    }

    let canceled = false;
    void refreshFileTree(() => canceled, { showLoading: fileTree.length === 0, markRefresh: true });

    const timer = window.setInterval(() => {
      void refreshFileTree(() => canceled, { showLoading: false, markRefresh: true });
    }, CODE_REFRESH_INTERVAL_MS);

    return () => {
      canceled = true;
      window.clearInterval(timer);
    };
  }, [fileTree.length, isAgentOutputting, refreshFileTree]);

  useEffect(() => {
    let canceled = false;

    const persistViewMode = async () => {
      try {
        await updateProject(projectId, {
          metadata: {
            last_view_mode: viewMode,
          },
        }, accessToken);
      } catch {
        if (!canceled) {
          // Ignore persistence errors in the prototype UI.
        }
      }
    };

    void persistViewMode();

    return () => {
      canceled = true;
    };
  }, [accessToken, projectId, viewMode]);

  const sendMessage = async () => {
    const normalized = chatInput.trim();
    if (!normalized || chatSubmitting) {
      return;
    }

    setChatError(null);
    setChatSubmitting(true);
    setIsChatOutputting(true);

    const now = new Date().toISOString();
    const optimisticUser: ChatMessage = {
      id: `local_user_${Date.now()}`,
      role: 'user',
      content: normalized,
      created_at: now,
    };
    const streamingAssistant: ChatMessage = {
      id: `local_assistant_${Date.now()}`,
      role: 'assistant',
      content: '',
      created_at: now,
    };
    let streamedAssistantText = '';

    setMessages((previous) => [...previous, optimisticUser, streamingAssistant]);
    setChatInput('');

    try {
      const result = await sendChatMessage(projectId, normalized, accessToken, {
        deep: deepEnabled,
        onDelta: (fullText) => {
          streamedAssistantText = fullText;
          setMessages((previous) =>
            previous.map((message) =>
              message.id === streamingAssistant.id
                ? {
                    ...message,
                    content: fullText,
                  }
                : message,
            ),
          );
        },
      });

      setMessages((previous) => {
        const withoutStreaming = previous.filter(
          (msg) => msg.id !== optimisticUser.id && msg.id !== streamingAssistant.id,
        );
        const next = [...withoutStreaming, optimisticUser];

        if (result.assistant_message) {
          next.push({
            ...result.assistant_message,
            content: result.assistant_message.content || streamedAssistantText,
            created_at: result.assistant_message.created_at || streamingAssistant.created_at,
          });
        } else {
          next.push({
            ...streamingAssistant,
            content: streamedAssistantText,
          });
        }

        return next;
      });
    } catch (error) {
      setMessages((previous) =>
        previous.filter((msg) => msg.id !== optimisticUser.id && msg.id !== streamingAssistant.id),
      );
      setChatError((error as Error).message || 'Failed to send message.');
    } finally {
      setIsChatOutputting(false);
      setChatSubmitting(false);
    }
  };

  const handleOpenFile = useCallback((path: string) => getFileContent(projectId, path, accessToken), [accessToken, projectId]);
  const handleDownloadProject = useCallback(() => downloadProject(projectId, projectName, accessToken), [accessToken, projectId, projectName]);

  const messageItems = useMemo(() => {
    const normalizedPrompt = initialPrompt.trim();
    const filteredMessages = generationJobId
      ? messages.filter((message) => !(message.role === 'assistant' && !message.content.trim()))
      : messages;
    const merged = [...filteredMessages];

    const pendingUser = pendingGenerationMessages.find((message) => message.role === 'user');
    if (pendingUser && !filteredMessages.some((message) => message.role === 'user' && message.content.trim() === pendingUser.content.trim())) {
      merged.push(pendingUser);
    }

    const pendingAssistant = pendingGenerationMessages.find((message) => message.role === 'assistant');
    if (pendingAssistant) {
      const pendingAssistantContent = pendingAssistant.content.trim();
      const hasSameAssistant = pendingAssistantContent !== ''
        && filteredMessages.some((message) => message.role === 'assistant' && message.content.trim() === pendingAssistantContent);
      if (!hasSameAssistant) {
        if (pendingAssistantContent !== '' || (!normalizedPrompt && !filteredMessages.some((message) => message.role === 'assistant'))) {
          merged.push(pendingAssistant);
        }
      }
    }

    if (merged.length > 0) {
      return sortChatMessages(merged);
    }

    if (bootstrapLoading || Boolean(generationJobId)) {
      return [];
    }

    return [
      {
        id: 'local_seed_message',
        role: 'assistant' as const,
        content: 'Workspace is ready. Ask me what to build or change next.',
        created_at: new Date().toISOString(),
      },
    ];
  }, [bootstrapLoading, generationJobId, initialPrompt, messages, pendingGenerationMessages]);

  return (
    <motion.div
      initial={{ opacity: 0, y: 20, scale: 0.98 }}
      animate={{ opacity: 1, y: 0, scale: 1 }}
      transition={{ duration: 0.35, ease: [0.16, 1, 0.3, 1] }}
      className="h-screen overflow-hidden flex flex-col bg-[#0B1120] text-white font-sans"
    >
      <header className="flex items-center justify-between px-6 py-3 border-b border-slate-800/50 bg-[#0B1120] z-10 shrink-0">
        <div className="flex items-center gap-4">
          <button
            onClick={onBack}
            className="p-2 -ml-2 text-slate-400 hover:text-white transition-colors rounded-lg hover:bg-slate-800/50"
          >
            <ArrowLeft size={20} />
          </button>
          <div className="flex items-center gap-2">
            <div className="w-6 h-6 text-blue-500">
              <svg viewBox="0 0 24 24" fill="currentColor">
                <path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5" />
              </svg>
            </div>
            <span className="text-lg font-bold tracking-tight">Agentland</span>
          </div>
          <div className="w-px h-5 bg-slate-700 mx-2"></div>
          <span className="text-slate-400 text-sm truncate max-w-[320px]">{projectName}</span>
        </div>
        <div className="flex items-center gap-6">
          <div className="flex items-center gap-3 border-r border-slate-800 pr-6">
            <button className="text-sm font-medium text-slate-300 hover:text-white px-3 py-1.5 rounded-lg hover:bg-slate-800/50 transition-colors">
              {t('nav.share')}
            </button>
            <button className="text-sm font-medium bg-blue-600 hover:bg-blue-700 text-white px-4 py-1.5 rounded-lg transition-colors shadow-lg shadow-blue-500/20">
              {t('nav.publish')}
            </button>
          </div>
          <div className="flex items-center gap-6">
            <LanguageSwitcher />
            <button onClick={onProjects} className="flex items-center gap-2 text-sm text-slate-400 hover:text-slate-200 transition-colors">
              <Folder size={18} />
              <span>{t('nav.projects')}</span>
            </button>
            <button className="flex items-center gap-2 text-sm text-slate-400 hover:text-slate-200 transition-colors">
              <HelpCircle size={18} />
              <span>{t('nav.docs')}</span>
            </button>
            <UserMenu currentUser={currentUser} onLogout={onLogout} />
          </div>
        </div>
      </header>

      <div className="flex-1 min-h-0 flex overflow-hidden">
        <aside className="w-[360px] min-h-0 flex flex-col border-r border-slate-800/50 bg-[#0B1120] shrink-0">
          <div className="px-5 py-4 border-b border-slate-800/50 flex items-center justify-between shrink-0">
            <div className="flex items-center gap-2">
              <Bot size={20} className="text-blue-500" />
              <span className="font-semibold">{t('workspace.codingAgent')}</span>
            </div>
          </div>


          <div className="chat-scrollbar flex-1 min-h-0 overflow-y-auto p-5 flex flex-col gap-4">
            {bootstrapLoading && (
              <div className="text-sm text-slate-400 flex items-center gap-2">
                <Loader2 size={14} className="animate-spin" /> Loading workspace...
              </div>
            )}

            {bootstrapError && (
              <div className="rounded-xl border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-200 flex flex-col gap-3">
                <span className="flex items-center gap-2">
                  <AlertCircle size={14} /> {bootstrapError}
                </span>
                <button
                  onClick={() => setReloadKey((previous) => previous + 1)}
                  className="self-start px-3 py-1.5 text-xs rounded-md bg-red-500/20 hover:bg-red-500/30"
                >
                  Retry
                </button>
              </div>
            )}


            {!bootstrapError &&
              messageItems.map((message) => {
                const isUser = message.role === 'user';
                return (
                  <div key={message.id} className={`flex flex-col gap-1.5 ${isUser ? 'items-end' : ''}`}>
                    <div className="flex items-center gap-2 text-xs text-slate-500">
                      {!isUser && (
                        <div className="w-5 h-5 rounded-full bg-blue-600 flex items-center justify-center text-white">
                          <Bot size={12} />
                        </div>
                      )}
                      <span>{isUser ? `You • ${messageTime(message.created_at)}` : `Agent • ${messageTime(message.created_at)}`}</span>
                    </div>
                    <div
                      className={`text-sm p-3.5 rounded-2xl leading-relaxed max-w-[95%] ${
                        isUser
                          ? 'bg-blue-600 text-white rounded-tr-sm'
                          : 'bg-[#1E293B] text-slate-200 rounded-tl-sm'
                      }`}
                    >
                      {message.content}
                    </div>
                  </div>
                );
              })}
          </div>

          <div className="p-4 shrink-0 border-t border-slate-800/50">
            <div className="bg-[#1E293B] rounded-xl p-3 flex flex-col gap-3">
              <textarea
                placeholder={t('workspace.askPlaceholder')}
                className="w-full bg-transparent text-sm text-slate-200 placeholder:text-slate-500 resize-none outline-none min-h-[60px]"
                value={chatInput}
                onChange={(event) => setChatInput(event.target.value)}
                disabled={chatSubmitting || Boolean(bootstrapError)}
              />
              <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                <DeepToggle
                  checked={deepEnabled}
                  onChange={onDeepEnabledChange}
                  disabled={chatSubmitting || Boolean(bootstrapError)}
                />
                <button
                  onClick={sendMessage}
                  disabled={chatSubmitting || !chatInput.trim() || Boolean(bootstrapError)}
                  className="bg-blue-600 hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed text-white px-4 py-1.5 rounded-lg text-sm font-medium flex items-center gap-2 transition-colors"
                >
                  {chatSubmitting ? <Loader2 size={14} className="animate-spin" /> : <Send size={14} />}
                  {t('workspace.send')}
                </button>
              </div>
              {chatError && <div className="text-xs text-red-300">{chatError}</div>}
            </div>
          </div>
        </aside>

        <main className="flex-1 min-h-0 flex flex-col min-w-0 bg-[#0B1120]">
          <div className="flex items-center justify-between px-6 py-2 border-b border-slate-800/50 shrink-0">
            <div className="flex items-center gap-6">
              <button
                onClick={() => setViewMode('preview')}
                className={`flex items-center gap-2 font-medium relative py-3 ${
                  viewMode === 'preview' ? 'text-blue-500' : 'text-slate-500 hover:text-slate-300 transition-colors'
                }`}
              >
                <Eye size={16} />
                <span>{t('nav.preview')}</span>
                {viewMode === 'preview' && <div className="absolute bottom-0 left-0 right-0 h-0.5 bg-blue-500"></div>}
              </button>
              <button
                onClick={() => setViewMode('code')}
                className={`flex items-center gap-2 font-medium relative py-3 ${
                  viewMode === 'code' ? 'text-blue-500' : 'text-slate-500 hover:text-slate-300 transition-colors'
                }`}
              >
                <Code2 size={16} />
                <span>{t('nav.code')}</span>
                {viewMode === 'code' && <div className="absolute bottom-0 left-0 right-0 h-0.5 bg-blue-500"></div>}
              </button>
            </div>
            <div className="flex items-center gap-4">
              <div className="flex items-center bg-slate-800/50 rounded-lg p-1">
                <button className="p-1.5 bg-slate-700 text-white rounded-md shadow-sm">
                  <Monitor size={14} />
                </button>
                <button className="p-1.5 text-slate-500 hover:text-slate-300">
                  <Tablet size={14} />
                </button>
                <button className="p-1.5 text-slate-500 hover:text-slate-300">
                  <Smartphone size={14} />
                </button>
              </div>
              <button className="bg-blue-600 hover:bg-blue-700 text-white px-4 py-1.5 rounded-lg text-sm font-medium flex items-center gap-2 transition-colors">
                <Rocket size={14} /> {t('nav.deploy')}
              </button>
            </div>
          </div>

          <div className="flex-1 p-6 overflow-hidden flex flex-col">
            {viewMode === 'preview' ? (
              <div className="flex-1 w-full max-w-6xl mx-auto bg-[#111827] rounded-xl overflow-hidden border border-slate-800 shadow-2xl">
                <div className="h-10 bg-[#0F172A] border-b border-slate-800 px-3 flex items-center gap-3 text-xs text-slate-400">
                  <div className="w-2 h-2 rounded-full bg-red-500/80"></div>
                  <div className="w-2 h-2 rounded-full bg-yellow-500/80"></div>
                  <div className="w-2 h-2 rounded-full bg-green-500/80"></div>
                  <div className="ml-2 truncate">{previewUrl || 'starting preview...'}</div>
                  <div className="ml-auto uppercase tracking-wide">{previewStatus}</div>
                </div>

                <div className="h-[calc(100%-40px)] bg-[#0B1120] relative">
                  {previewUrl ? (
                    <>
                      <iframe
                        title="project-preview"
                        src={previewUrl}
                        className="w-full h-full border-0 bg-white"
                        sandbox="allow-same-origin allow-scripts allow-forms allow-popups"
                      />
                      {previewLoading && (
                        <div className="absolute inset-0 bg-[#0B1120]/45 backdrop-blur-[1px] flex items-center justify-center text-slate-100 gap-2 text-sm pointer-events-none">
                          <Loader2 size={16} className="animate-spin" /> {t('workspace.previewBooting')}
                        </div>
                      )}
                    </>
                  ) : previewLoading ? (
                    <div className="h-full flex items-center justify-center text-slate-300 gap-2 text-sm">
                      <Loader2 size={16} className="animate-spin" /> {t('workspace.previewBooting')}
                    </div>
                  ) : previewError ? (
                    <div className="h-full flex items-center justify-center text-red-300 gap-2 text-sm">
                      <AlertCircle size={14} /> {previewError}
                    </div>
                  ) : (
                    <div className="h-full flex items-center justify-center text-slate-500 text-sm">No preview URL available.</div>
                  )}
                </div>
              </div>
            ) : (
              <div className="flex-1 flex flex-col bg-[#1e1e1e] overflow-hidden rounded-xl border border-slate-800 shadow-2xl">
                <CodeEditor
                  tree={fileTree}
                  loading={fileLoading}
                  error={fileError}
                  refreshSignal={codeRefreshSignal}
                  onOpenFile={handleOpenFile}
                  onDownloadProject={handleDownloadProject}
                />
              </div>
            )}
          </div>
        </main>
      </div>
    </motion.div>
  );
}
