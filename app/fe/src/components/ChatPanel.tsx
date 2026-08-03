import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertCircle, Bot, Check, Circle, LoaderCircle, Send, Square, Terminal, User, X } from 'lucide-react';
import {
  cancelRun,
  getRun,
  listMessages,
  startRun,
  streamRunEvents,
  type ChatMessage,
  type Project,
  type RunEvent,
  type RunStatus,
} from '../api';
import { useI18n } from '../i18n';
import { queryKeys } from '../queryKeys';

type ToolActivity = {
  id: string;
  name: string;
  status: 'running' | 'completed' | 'failed';
  output: string;
};

type ChatPanelProps = {
  project: Project;
  readOnly: boolean;
};

function payloadString(payload: Record<string, unknown>, keys: string[]) {
  for (const key of keys) {
    const value = payload[key];
    if (typeof value === 'string') return value;
    if (value !== undefined && value !== null && key === 'output') {
      try {
        return JSON.stringify(value, null, 2);
      } catch {
        return String(value);
      }
    }
  }
  return '';
}

function eventToolId(event: RunEvent) {
  return payloadString(event.payload, ['tool_call_id', 'tool_id', 'id']) || `tool-${event.sequence}`;
}

function messageLabel(message: ChatMessage, agentLabel: string, userLabel: string) {
  if (message.role === 'user') return userLabel;
  if (message.role === 'assistant') return agentLabel;
  return message.role;
}

function isTerminalRun(status: RunStatus) {
  return status === 'completed' || status === 'failed' || status === 'cancelled';
}

function isTerminalRunEvent(event: RunEvent) {
  return event.type === 'run.completed' || event.type === 'run.failed' || event.type === 'run.cancelled';
}

function reconnectDelay(signal: AbortSignal) {
  return new Promise<void>((resolve) => {
    const timeout = window.setTimeout(resolve, 1_000);
    signal.addEventListener('abort', () => {
      window.clearTimeout(timeout);
      resolve();
    }, { once: true });
  });
}

function mergeRecoveredAssistant(persisted: string, streamed: string) {
  if (!persisted) return streamed;
  if (!streamed || persisted.startsWith(streamed)) return persisted;
  if (streamed.startsWith(persisted)) return streamed;

  const overlapLimit = Math.min(persisted.length, streamed.length);
  for (let overlap = overlapLimit; overlap > 0; overlap -= 1) {
    if (persisted.endsWith(streamed.slice(0, overlap))) {
      return `${persisted}${streamed.slice(overlap)}`;
    }
  }
  return `${persisted}${streamed}`;
}

export default function ChatPanel({ project, readOnly }: ChatPanelProps) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState('');
  const [activeRunId, setActiveRunId] = useState<string | null>(project.active_run_id ?? null);
  const [runStatus, setRunStatus] = useState<RunStatus | null>(project.active_run_id ? 'running' : null);
  const [pendingUser, setPendingUser] = useState<ChatMessage | null>(null);
  const [streamedAssistant, setStreamedAssistant] = useState<{ runId: string; content: string } | null>(null);
  const [tools, setTools] = useState<ToolActivity[]>([]);
  const [runError, setRunError] = useState('');
  const [cancelRequested, setCancelRequested] = useState(false);
  const lastSequenceRef = useRef(0);
  const activeRunIdRef = useRef(activeRunId);
  const projectRunIdRef = useRef(project.active_run_id ?? null);
  const chatScrollRef = useRef<HTMLDivElement>(null);
  const loadingEarlierRef = useRef(false);
  const endRef = useRef<HTMLDivElement>(null);

  const messagesQuery = useInfiniteQuery({
    queryKey: queryKeys.messages(project.id),
    queryFn: ({ pageParam }) => listMessages(project.id, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.next_cursor || undefined,
  });

  const persistedMessages = useMemo(() => {
    const seen = new Set<string>();
    return [...(messagesQuery.data?.pages ?? [])]
      .reverse()
      .flatMap((page) => page.items)
      .filter((message) => {
        if (seen.has(message.id)) return false;
        seen.add(message.id);
        return true;
      });
  }, [messagesQuery.data]);

  useEffect(() => {
    if (pendingUser && persistedMessages.some((message) => message.id === pendingUser.id)) setPendingUser(null);
    if (streamedAssistant) {
      const persisted = persistedMessages.find((message) => message.run_id === streamedAssistant.runId && message.role === 'assistant');
      if (persisted?.status === 'completed' || (persisted?.content && persisted.content === streamedAssistant.content)) {
        setStreamedAssistant(null);
      }
    }
  }, [pendingUser, persistedMessages, streamedAssistant]);

  const invalidateWorkspaceFiles = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: queryKeys.files(project.id) });
    void queryClient.invalidateQueries({ queryKey: queryKeys.fileContents(project.id) });
  }, [project.id, queryClient]);

  const finishRun = useCallback((runId: string, status: RunStatus | null, error = '') => {
    if (activeRunIdRef.current !== runId) return;
    activeRunIdRef.current = null;
    setRunStatus(status);
    setRunError(error);
    setActiveRunId(null);
    setCancelRequested(false);
    void queryClient.invalidateQueries({ queryKey: queryKeys.messages(project.id) });
    void queryClient.invalidateQueries({ queryKey: queryKeys.project(project.id), exact: true });
    invalidateWorkspaceFiles();
  }, [invalidateWorkspaceFiles, project.id, queryClient]);

  useEffect(() => {
    const nextRunId = project.active_run_id ?? null;
    const previousRunId = projectRunIdRef.current;
    projectRunIdRef.current = nextRunId;

    if (nextRunId) {
      if (activeRunIdRef.current !== nextRunId) {
        activeRunIdRef.current = nextRunId;
        lastSequenceRef.current = 0;
        setActiveRunId(nextRunId);
        setRunStatus('running');
        setRunError('');
        setCancelRequested(false);
      }
      return;
    }
    if (previousRunId) finishRun(previousRunId, null);
  }, [finishRun, project.active_run_id]);

  const handleEvent = useCallback((event: RunEvent) => {
    if (event.sequence > 0 && event.sequence <= lastSequenceRef.current) return;
    if (event.sequence > 0) lastSequenceRef.current = event.sequence;

    if (event.type === 'run.started') setRunStatus('running');
    if (event.type === 'message.delta') {
      const delta = payloadString(event.payload, ['delta', 'text', 'content']);
      if (delta) {
        setStreamedAssistant((current) => ({
          runId: event.run_id,
          content: `${current?.runId === event.run_id ? current.content : ''}${delta}`,
        }));
      }
    }
    if (event.type === 'tool.started') {
      const id = eventToolId(event);
      const name = payloadString(event.payload, ['name', 'tool_name']) || 'tool';
      setTools((current) => [...current.filter((tool) => tool.id !== id), { id, name, status: 'running', output: '' }]);
    }
    if (event.type === 'tool.output') {
      const id = eventToolId(event);
      const output = payloadString(event.payload, ['output', 'delta', 'text', 'content']);
      setTools((current) => current.map((tool) => tool.id === id ? { ...tool, output: `${tool.output}${output}` } : tool));
    }
    if (event.type === 'tool.completed') {
      const id = eventToolId(event);
      const failed = Boolean(event.payload.error) || event.payload.status === 'failed';
      setTools((current) => current.map((tool) => tool.id === id ? { ...tool, status: failed ? 'failed' : 'completed' } : tool));
      invalidateWorkspaceFiles();
    }
    if (event.type === 'run.completed') finishRun(event.run_id, 'completed');
    if (event.type === 'run.cancelled') finishRun(event.run_id, 'cancelled');
    if (event.type === 'run.failed') finishRun(event.run_id, 'failed', payloadString(event.payload, ['error', 'message']));
  }, [finishRun, invalidateWorkspaceFiles]);

  const runQuery = useQuery({
    queryKey: queryKeys.run(activeRunId ?? ''),
    queryFn: () => getRun(activeRunId!),
    enabled: Boolean(activeRunId),
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      return status && isTerminalRun(status) ? false : 2_000;
    },
    refetchIntervalInBackground: true,
  });

  useEffect(() => {
    const run = runQuery.data;
    if (!activeRunId || !run || run.id !== activeRunId) return;
    if (isTerminalRun(run.status)) {
      finishRun(run.id, run.status, run.error_message || run.error || '');
      return;
    }
    setRunStatus(run.status);
  }, [activeRunId, finishRun, runQuery.data]);

  useEffect(() => {
    if (!activeRunId) return;
    const controller = new AbortController();
    lastSequenceRef.current = 0;

    void (async () => {
      let lastEventId = '';
      let terminalEventReceived = false;
      while (!controller.signal.aborted && !terminalEventReceived && activeRunIdRef.current === activeRunId) {
        try {
          await streamRunEvents(activeRunId, {
            signal: controller.signal,
            lastEventId,
            onLastEventId: (id) => { lastEventId = id; },
            onEvent: (event) => {
              if (isTerminalRunEvent(event)) terminalEventReceived = true;
              handleEvent(event);
            },
          });
        } catch (error) {
          if (controller.signal.aborted) return;
          setRunError(error instanceof Error && error.message ? error.message : t('workspace.connectionError'));
        }
        if (!terminalEventReceived && !controller.signal.aborted) await reconnectDelay(controller.signal);
      }
    })();

    return () => controller.abort();
  }, [activeRunId, handleEvent, t]);

  useEffect(() => {
    if (loadingEarlierRef.current) return;
    endRef.current?.scrollIntoView({ block: 'end' });
  }, [messagesQuery.data, pendingUser, streamedAssistant, tools, runStatus]);

  const loadEarlier = async () => {
    const container = chatScrollRef.current;
    const previousHeight = container?.scrollHeight ?? 0;
    const previousTop = container?.scrollTop ?? 0;
    loadingEarlierRef.current = true;
    try {
      await messagesQuery.fetchNextPage();
      window.requestAnimationFrame(() => {
        if (container) container.scrollTop = previousTop + container.scrollHeight - previousHeight;
        loadingEarlierRef.current = false;
      });
    } catch {
      loadingEarlierRef.current = false;
    }
  };

  const startMutation = useMutation({
    mutationFn: async (message: string) => {
      const idempotencyKey = crypto.randomUUID();
      return startRun(project.id, message, idempotencyKey);
    },
    onSuccess: (run, message) => {
      setPendingUser({ id: run.user_message_id, run_id: run.run_id, role: 'user', content: message, status: 'completed', created_at: new Date().toISOString() });
      setStreamedAssistant({ runId: run.run_id, content: '' });
      setTools([]);
      setRunError('');
      setRunStatus(run.status);
      setCancelRequested(false);
      activeRunIdRef.current = run.run_id;
      setActiveRunId(run.run_id);
      setDraft('');
      void queryClient.invalidateQueries({ queryKey: queryKeys.project(project.id), exact: true });
    },
  });

  const cancelMutation = useMutation({
    mutationFn: (runId: string) => cancelRun(runId),
    onMutate: () => setRunError(''),
    onSuccess: (result, runId) => {
      if (result.status === 'completed' || result.status === 'failed' || result.status === 'cancelled') {
        finishRun(runId, result.status);
        return;
      }
      setRunStatus(result.status);
      setCancelRequested(true);
    },
    onError: (error) => setRunError(error.message),
  });

  const submit = (event?: FormEvent) => {
    event?.preventDefault();
    const message = draft.trim();
    if (!message || activeRunId || readOnly || startMutation.isPending) return;
    startMutation.mutate(message);
  };

  const persistedActiveAssistant = streamedAssistant
    ? persistedMessages.find((message) => message.run_id === streamedAssistant.runId && message.role === 'assistant')
    : undefined;
  const recoveredAssistantContent = streamedAssistant
    ? mergeRecoveredAssistant(persistedActiveAssistant?.content ?? '', streamedAssistant.content)
    : '';
  const visibleMessages = useMemo(() => streamedAssistant?.content
    ? persistedMessages.filter((message) => message.role !== 'assistant' || message.run_id !== streamedAssistant.runId)
    : persistedMessages, [persistedMessages, streamedAssistant]);
  const showPendingUser = pendingUser && !persistedMessages.some((message) => message.id === pendingUser.id);
  const isRunning = Boolean(activeRunId) && (runStatus === 'queued' || runStatus === 'running');

  return (
    <section aria-label={t('workspace.chat')} className="flex h-full min-h-0 flex-col bg-white">
      <div className="flex h-11 shrink-0 items-center justify-between border-b border-slate-200 px-4">
        <div className="flex items-center gap-2 text-sm font-medium text-slate-800"><Bot size={16} />{t('workspace.agent')}</div>
        {isRunning && (
          <button
            type="button"
            onClick={() => cancelMutation.mutate(activeRunId!)}
            disabled={cancelMutation.isPending || cancelRequested}
            className="flex h-8 items-center gap-2 rounded-md border border-slate-300 px-2.5 text-xs font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-60"
          >
            {cancelMutation.isPending || cancelRequested ? <LoaderCircle size={13} className="animate-spin" /> : <Square size={12} fill="currentColor" />}
            {cancelMutation.isPending || cancelRequested ? t('workspace.stopping') : t('workspace.stop')}
          </button>
        )}
      </div>

      <div ref={chatScrollRef} className="chat-scrollbar min-h-0 flex-1 overflow-y-auto px-4 py-5">
        {messagesQuery.isLoading && <div className="flex items-center justify-center py-12 text-sm text-slate-500"><LoaderCircle size={17} className="mr-2 animate-spin" />{t('common.loading')}</div>}
        {messagesQuery.isError && <div role="alert" className="rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-800">{messagesQuery.error.message}</div>}
        {!messagesQuery.isLoading && visibleMessages.length === 0 && !pendingUser && !streamedAssistant && (
          <div className="mx-auto flex max-w-sm flex-col items-center py-16 text-center text-sm leading-6 text-slate-500"><Bot size={28} className="mb-3 text-slate-400" />{t('workspace.empty')}</div>
        )}

        <div className="space-y-5">
          {messagesQuery.hasNextPage && (
            <div className="flex justify-center">
              <button
                type="button"
                onClick={() => void loadEarlier()}
                disabled={messagesQuery.isFetchingNextPage}
                className="flex h-8 items-center gap-2 rounded-md border border-slate-300 px-3 text-xs font-medium text-slate-600 hover:bg-slate-50 disabled:opacity-60"
              >
                {messagesQuery.isFetchingNextPage && <LoaderCircle size={13} className="animate-spin" />}
                {t('workspace.loadEarlier')}
              </button>
            </div>
          )}
          {visibleMessages.map((message) => <MessageView key={message.id} message={message} agentLabel={t('workspace.agent')} userLabel={t('workspace.you')} />)}
          {showPendingUser && <MessageView message={pendingUser} agentLabel={t('workspace.agent')} userLabel={t('workspace.you')} />}
          {streamedAssistant?.content && recoveredAssistantContent && (
            <MessageView message={{ id: `stream-${streamedAssistant.runId}`, run_id: streamedAssistant.runId, role: 'assistant', content: recoveredAssistantContent, status: 'streaming', created_at: new Date().toISOString() }} agentLabel={t('workspace.agent')} userLabel={t('workspace.you')} />
          )}

          {tools.length > 0 && (
            <div className="space-y-2" aria-label="Tool activity">
              {tools.map((tool) => <ToolView key={tool.id} tool={tool} />)}
            </div>
          )}

          {isRunning && !streamedAssistant?.content && (
            <div className="flex items-center gap-2 text-sm text-slate-500"><LoaderCircle size={15} className="animate-spin" />{t('workspace.running')}</div>
          )}
          {runStatus === 'failed' && <RunNotice tone="error" text={`${t('workspace.failed')}${runError ? `: ${runError}` : ''}`} />}
          {runStatus === 'cancelled' && <RunNotice tone="neutral" text={t('workspace.cancelled')} />}
          {runError && runStatus !== 'failed' && <RunNotice tone="error" text={runError} />}
          <div ref={endRef} />
        </div>
      </div>

      <form onSubmit={submit} className="shrink-0 border-t border-slate-200 bg-white p-3">
        <div className="relative rounded-md border border-slate-300 bg-white focus-within:border-slate-500">
          <textarea
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter' && !event.shiftKey) {
                event.preventDefault();
                submit();
              }
            }}
			disabled={readOnly || isRunning || startMutation.isPending}
            placeholder={t('workspace.placeholder')}
            rows={3}
            className="block max-h-40 min-h-20 w-full resize-none rounded-md bg-transparent px-3 py-2.5 pr-12 text-sm leading-5 outline-none placeholder:text-slate-500 disabled:bg-slate-50"
          />
          <button
            type="submit"
            disabled={!draft.trim() || readOnly || isRunning || startMutation.isPending}
            aria-label={t('workspace.send')}
            className="absolute bottom-2 right-2 flex h-8 w-8 items-center justify-center rounded-md bg-slate-900 text-white hover:bg-slate-800 disabled:bg-slate-300"
          >
            {startMutation.isPending ? <LoaderCircle size={15} className="animate-spin" /> : <Send size={15} />}
          </button>
        </div>
        {startMutation.isError && <p role="alert" className="mt-2 text-xs text-red-700">{startMutation.error.message}</p>}
      </form>
    </section>
  );
}

function MessageView({ message, agentLabel, userLabel }: { message: ChatMessage; agentLabel: string; userLabel: string }) {
  const isUser = message.role === 'user';
  return (
    <article className={`flex gap-3 ${isUser ? 'flex-row-reverse' : ''}`}>
      <span className={`flex h-7 w-7 shrink-0 items-center justify-center rounded-md ${isUser ? 'bg-slate-200 text-slate-700' : 'bg-slate-900 text-white'}`}>
        {isUser ? <User size={14} /> : <Bot size={14} />}
      </span>
      <div className={`min-w-0 max-w-[88%] ${isUser ? 'text-right' : ''}`}>
        <div className="mb-1 text-xs font-medium text-slate-500">{messageLabel(message, agentLabel, userLabel)}</div>
        <div className={`whitespace-pre-wrap break-words rounded-md px-3 py-2 text-left text-sm leading-6 ${isUser ? 'bg-slate-100 text-slate-800' : 'border border-slate-200 bg-white text-slate-800'}`}>{message.content}</div>
      </div>
    </article>
  );
}

function ToolView({ tool }: { tool: ToolActivity }) {
  const { t } = useI18n();
  const icon = tool.status === 'running' ? <Circle size={12} className="animate-pulse fill-sky-400 text-sky-400" /> : tool.status === 'completed' ? <Check size={13} className="text-emerald-400" /> : <X size={13} className="text-red-400" />;
  const status = tool.status === 'running' ? t('workspace.toolRunning') : tool.status === 'completed' ? t('workspace.toolDone') : t('workspace.toolFailed');
  return (
    <details className="overflow-hidden rounded-md border border-slate-700 bg-slate-950 text-slate-200" open={tool.status === 'running'}>
      <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2 text-xs"><Terminal size={13} /> <span className="min-w-0 flex-1 truncate font-mono">{tool.name}</span>{icon}<span className="text-slate-400">{status}</span></summary>
      {tool.output && <pre className="chat-scrollbar max-h-48 overflow-auto border-t border-slate-800 p-3 text-xs leading-5 text-slate-300">{tool.output}</pre>}
    </details>
  );
}

function RunNotice({ tone, text }: { tone: 'error' | 'neutral'; text: string }) {
  return <div role="status" className={`flex items-start gap-2 rounded-md border p-3 text-sm ${tone === 'error' ? 'border-red-200 bg-red-50 text-red-800' : 'border-slate-200 bg-slate-50 text-slate-700'}`}><AlertCircle size={16} className="mt-0.5 shrink-0" />{text}</div>;
}
