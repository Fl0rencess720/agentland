import { useCallback, useEffect, useMemo, useState } from 'react';
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
  User,
  Loader2,
  AlertCircle,
  Rocket,
} from 'lucide-react';
import CodeEditor from './CodeEditor';
import { useI18n } from '../i18n';
import LanguageSwitcher from './LanguageSwitcher';
import {
  getChatConversations,
  getChatMessages,
  getFileContent,
  downloadProject,
  getFileTree,
  getPreview,
  sendChatMessage,
  sleep,
  startPreview,
  updateProject,
  type ChatMessage,
  type FileTreeNode,
} from '../api';

type WorkspaceProps = {
  onBack: () => void;
  onProjects: () => void;
  onLogout: () => void;
  projectId: string;
  projectName: string;
  initialPrompt: string;
  initialViewMode?: 'preview' | 'code';
  accessToken?: string;
};

const PREVIEW_POLL_INTERVAL_MS = 1500;
const PREVIEW_POLL_MAX_ATTEMPTS = 20;

function messageTime(input?: string) {
  if (!input) return 'just now';
  const date = new Date(input);
  if (Number.isNaN(date.getTime())) return 'just now';
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

export default function Workspace({
  onBack,
  onProjects,
  onLogout,
  projectId,
  projectName,
  initialPrompt,
  initialViewMode = 'preview',
  accessToken,
}: WorkspaceProps) {
  const [viewMode, setViewMode] = useState<'preview' | 'code'>(initialViewMode);
  const { t } = useI18n();

  const [reloadKey, setReloadKey] = useState(0);

  const [bootstrapLoading, setBootstrapLoading] = useState(true);
  const [bootstrapError, setBootstrapError] = useState<string | null>(null);

  const [conversationId, setConversationId] = useState('c_default');
  const [messages, setMessages] = useState<ChatMessage[]>([]);
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

  const loadWorkspace = useCallback(async (isCanceled: () => boolean) => {
    setBootstrapLoading(true);
    setBootstrapError(null);
    setChatError(null);

    setFileLoading(true);
    setFileError(null);

    setPreviewLoading(true);
    setPreviewError(null);

    try {
      const conversationData = await getChatConversations(projectId, accessToken);
      const availableConversations = conversationData.items ?? [];
      const initialConversationId = availableConversations[0]?.id ?? 'c_default';

      const [chatData, treeData, previewData] = await Promise.all([
        getChatMessages(projectId, initialConversationId, '', accessToken),
        getFileTree(projectId, '/workspace', 3, accessToken),
        startPreview(projectId, accessToken),
      ]);

      if (isCanceled()) return;

      setConversationId(chatData.conversation_id || initialConversationId);
      setMessages(chatData.items ?? []);

      setFileTree(treeData.nodes ?? []);
      setFileLoading(false);

      setPreviewId(previewData.preview_id ?? null);
      setPreviewStatus(previewData.status ?? 'STARTING');
      setPreviewUrl(previewData.preview_url ?? null);
      setPreviewLoading((previewData.status ?? 'STARTING') !== 'RUNNING');

      if (previewData.preview_id && (previewData.status ?? 'STARTING') !== 'RUNNING') {
        void (async () => {
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

          if (!isCanceled()) {
            setPreviewLoading(false);
            setPreviewError('Preview startup timeout.');
          }
        })();
      } else {
        setPreviewLoading(false);
      }
    } catch (error) {
      if (isCanceled()) return;

      const message = (error as Error).message || 'Failed to load workspace.';
      setBootstrapError(message);
      setFileError(message);
      setPreviewError(message);
      setFileLoading(false);
      setPreviewLoading(false);
    } finally {
      if (!isCanceled()) {
        setBootstrapLoading(false);
      }
    }
  }, [accessToken, projectId]);

  useEffect(() => {
    let canceled = false;
    void loadWorkspace(() => canceled);
    return () => {
      canceled = true;
    };
  }, [loadWorkspace, reloadKey]);

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
      const result = await sendChatMessage(projectId, conversationId, normalized, accessToken, {
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
      setChatSubmitting(false);
    }
  };

  const messageItems = useMemo(() => {
    if (messages.length > 0) {
      return messages;
    }

    return [
      {
        id: 'local_seed_message',
        role: 'assistant' as const,
        content: initialPrompt
          ? `I have started generating based on your prompt: ${initialPrompt}`
          : 'Workspace is ready. Ask me what to build or change next.',
        created_at: new Date().toISOString(),
      },
    ];
  }, [messages, initialPrompt]);

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
            <button
              onClick={onLogout}
              className="w-8 h-8 rounded-full bg-slate-800 flex items-center justify-center text-slate-300 hover:text-white hover:bg-slate-700 transition-all"
            >
              <User size={18} />
            </button>
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
              <div className="flex items-center justify-end">
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

                <div className="h-[calc(100%-40px)] bg-[#0B1120]">
                  {previewLoading ? (
                    <div className="h-full flex items-center justify-center text-slate-300 gap-2 text-sm">
                      <Loader2 size={16} className="animate-spin" /> Booting preview...
                    </div>
                  ) : previewError ? (
                    <div className="h-full flex items-center justify-center text-red-300 gap-2 text-sm">
                      <AlertCircle size={14} /> {previewError}
                    </div>
                  ) : previewUrl ? (
                    <iframe
                      title="project-preview"
                      src={previewUrl}
                      className="w-full h-full border-0 bg-white"
                      sandbox="allow-same-origin allow-scripts allow-forms allow-popups"
                    />
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
                  onOpenFile={(path) => getFileContent(projectId, path, accessToken)}
                  onDownloadProject={() => downloadProject(projectId, projectName, accessToken)}
                />
              </div>
            )}
          </div>
        </main>
      </div>
    </motion.div>
  );
}
