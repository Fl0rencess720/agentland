import { useCallback, useEffect, useMemo, useState } from 'react';
import Editor from '@monaco-editor/react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertTriangle, Check, ChevronRight, File, Folder, FolderOpen, LoaderCircle, RefreshCw, Save } from 'lucide-react';
import { ApiError, getFileContent, getFileTree, updateFileContent, type FileTreeNode } from '../api';
import { useI18n } from '../i18n';
import { queryKeys } from '../queryKeys';
import { fileDraftKey, useWorkspaceStore } from '../stores/workspace';

type CodeEditorProps = {
  projectId: string;
  readOnly: boolean;
};

type TreeNode = Omit<FileTreeNode, 'children'> & { children: TreeNode[] };

function normalizePath(path: string) {
  return path.replace(/^\.\//, '').replace(/^\/+/, '').replace(/\/$/, '');
}

function buildTree(nodes: FileTreeNode[]): TreeNode[] {
  const root: TreeNode = { path: '', name: '', type: 'dir', children: [] };
  const index = new Map<string, TreeNode>([['', root]]);

  const ensureNode = (path: string, type: FileTreeNode['type'] = 'dir') => {
    const cleanPath = normalizePath(path);
    const existing = index.get(cleanPath);
    if (existing) return existing;
    const segments = cleanPath.split('/').filter(Boolean);
    const name = segments.at(-1) ?? cleanPath;
    const parentPath = segments.slice(0, -1).join('/');
    const parent = ensureNode(parentPath, 'dir');
    const node: TreeNode = { path: cleanPath, name, type, children: [] };
    parent.children.push(node);
    index.set(cleanPath, node);
    return node;
  };

  const visit = (node: FileTreeNode, parentPath = '') => {
    const rawPath = normalizePath(node.path || [parentPath, node.name].filter(Boolean).join('/'));
    const current = ensureNode(rawPath, node.type);
    current.name = node.name || current.name;
    current.type = node.type;
    current.size = node.size;
    current.mod_time = node.mod_time;
    node.children?.forEach((child) => visit(child, rawPath));
  };
  nodes.forEach((node) => visit(node));

  const sort = (items: TreeNode[]) => {
    items.sort((left, right) => {
      const leftDir = left.type !== 'file';
      const rightDir = right.type !== 'file';
      if (leftDir !== rightDir) return leftDir ? -1 : 1;
      return left.name.localeCompare(right.name);
    });
    items.forEach((item) => sort(item.children));
  };
  sort(root.children);
  return root.children;
}

function firstFile(nodes: TreeNode[]): string | null {
  for (const node of nodes) {
    if (node.type === 'file') return node.path;
    const child = firstFile(node.children);
    if (child) return child;
  }
  return null;
}

function languageFromPath(path: string, serverLanguage?: string) {
  if (serverLanguage) return serverLanguage;
  const extension = path.split('.').pop()?.toLowerCase();
  return ({ ts: 'typescript', tsx: 'typescript', js: 'javascript', jsx: 'javascript', json: 'json', css: 'css', html: 'html', go: 'go', py: 'python', md: 'markdown', yaml: 'yaml', yml: 'yaml', sh: 'shell' } as Record<string, string>)[extension ?? ''] ?? 'plaintext';
}

export default function CodeEditor({ projectId, readOnly }: CodeEditorProps) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { fileDrafts, selectedFile, setFileDraft, setSelectedFile } = useWorkspaceStore();
  const [content, setContent] = useState('');
  const [savedContent, setSavedContent] = useState('');
  const [loadedSha, setLoadedSha] = useState('');
  const [conflict, setConflict] = useState(false);
  const [saveMessage, setSaveMessage] = useState('');
  const [conflictResolutionError, setConflictResolutionError] = useState('');

  const treeQuery = useQuery({
    queryKey: queryKeys.files(projectId),
    queryFn: () => getFileTree(projectId),
    enabled: !readOnly,
  });
  const tree = useMemo(() => buildTree(treeQuery.data?.nodes ?? []), [treeQuery.data]);

  useEffect(() => {
    if (!selectedFile && tree.length) setSelectedFile(firstFile(tree));
  }, [selectedFile, setSelectedFile, tree]);

  const fileQuery = useQuery({
    queryKey: queryKeys.file(projectId, selectedFile ?? ''),
    queryFn: () => getFileContent(projectId, selectedFile!),
    enabled: Boolean(selectedFile) && !readOnly,
  });

  useEffect(() => {
    setSaveMessage('');
    setConflictResolutionError('');
  }, [selectedFile]);

  useEffect(() => {
    if (!fileQuery.data) return;
    const draft = selectedFile ? useWorkspaceStore.getState().fileDrafts[fileDraftKey(projectId, selectedFile)] : undefined;
    if (draft) {
      setContent(draft.content);
      setSavedContent(draft.baseContent);
      setLoadedSha(draft.baseSha);
      setConflict(draft.baseSha !== fileQuery.data.sha || draft.baseContent !== fileQuery.data.content);
      return;
    }
    setContent(fileQuery.data.content);
    setSavedContent(fileQuery.data.content);
    setLoadedSha(fileQuery.data.sha);
    setConflict(false);
  }, [fileQuery.data, projectId, selectedFile]);

  const saveMutation = useMutation({
    mutationFn: ({ path, nextContent, sha }: { path: string; nextContent: string; sha: string }) => updateFileContent(projectId, path, nextContent, sha),
    onSuccess: (saved, variables) => {
      queryClient.setQueryData(queryKeys.file(projectId, variables.path), (current: typeof fileQuery.data) => ({
        ...current,
        ...saved,
        path: variables.path,
        content: variables.nextContent,
        sha: saved.sha,
      }));
      const currentDraft = useWorkspaceStore.getState().fileDrafts[fileDraftKey(projectId, variables.path)];
      const newerDraft = currentDraft && currentDraft.content !== variables.nextContent ? {
        content: currentDraft.content,
        baseContent: variables.nextContent,
        baseSha: saved.sha,
      } : null;
      setFileDraft(projectId, variables.path, newerDraft);
      setConflictResolutionError('');
      void queryClient.invalidateQueries({ queryKey: queryKeys.files(projectId) });
      if (useWorkspaceStore.getState().selectedFile === variables.path) {
        setContent(newerDraft?.content ?? variables.nextContent);
        setSavedContent(variables.nextContent);
        setLoadedSha(saved.sha);
        setConflict(false);
        setSaveMessage(t('editor.saved'));
      }
    },
    onError: (error, variables) => {
      if (error instanceof ApiError && (error.status === 409 || error.code === 'FILE_CONFLICT') && useWorkspaceStore.getState().selectedFile === variables.path) {
        setConflict(true);
      }
    },
  });

  const dirty = Boolean(fileQuery.data && content !== savedContent);
  const save = useCallback(() => {
    if (!selectedFile || !loadedSha || !dirty || conflict || readOnly || saveMutation.isPending) return;
    setSaveMessage('');
    saveMutation.mutate({ path: selectedFile, nextContent: content, sha: loadedSha });
  }, [conflict, content, dirty, loadedSha, readOnly, saveMutation, selectedFile]);

  useEffect(() => {
    const keyboardSave = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 's') {
        event.preventDefault();
        save();
      }
    };
    window.addEventListener('keydown', keyboardSave);
    return () => window.removeEventListener('keydown', keyboardSave);
  }, [save]);

  const reloadLatest = async () => {
    if (!selectedFile) return;
    const path = selectedFile;
    setConflictResolutionError('');
    try {
      const latest = await getFileContent(projectId, path);
      queryClient.setQueryData(queryKeys.file(projectId, path), latest);
      setFileDraft(projectId, path, null);
      if (useWorkspaceStore.getState().selectedFile === path) {
        setContent(latest.content);
        setSavedContent(latest.content);
        setLoadedSha(latest.sha);
      }
      setConflict(false);
      setSaveMessage('');
    } catch (error) {
      setConflictResolutionError(error instanceof Error ? error.message : String(error));
    }
  };

  const overwriteLatest = async () => {
    if (!selectedFile) return;
    const path = selectedFile;
    const nextContent = content;
    setConflictResolutionError('');
    try {
      const latest = await getFileContent(projectId, path);
      saveMutation.mutate({ path, nextContent, sha: latest.sha });
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) {
        saveMutation.mutate({ path, nextContent, sha: '' });
        return;
      }
      setConflictResolutionError(error instanceof Error ? error.message : String(error));
    }
  };

  const draftPaths = useMemo(() => {
    const prefix = `${projectId}:`;
    return new Set(Object.keys(fileDrafts).filter((key) => key.startsWith(prefix)).map((key) => key.slice(prefix.length)));
  }, [fileDrafts, projectId]);

  if (readOnly) {
    return (
      <section aria-label={t('workspace.code')} className="flex h-full min-h-64 items-center justify-center bg-slate-950 p-6 text-center text-sm text-slate-400">
        <div><Folder size={25} className="mx-auto mb-3 text-slate-500" />{t('editor.runtimeUnavailable')}</div>
      </section>
    );
  }

  return (
    <section aria-label={t('workspace.code')} className="grid h-full min-h-0 grid-cols-[128px_minmax(0,1fr)] bg-slate-950 text-slate-100 sm:grid-cols-[minmax(150px,220px)_minmax(0,1fr)]">
      <aside className="min-h-0 overflow-y-auto border-r border-slate-800 bg-slate-900">
        <div className="sticky top-0 z-10 flex h-10 items-center justify-between border-b border-slate-800 bg-slate-900 px-3 text-xs font-medium uppercase text-slate-400">
          {t('editor.files')}
          <button type="button" onClick={() => void treeQuery.refetch()} aria-label={t('common.retry')} className="flex h-7 w-7 items-center justify-center rounded-md hover:bg-slate-800"><RefreshCw size={13} /></button>
        </div>
        {treeQuery.isLoading && <div className="flex items-center gap-2 p-3 text-xs text-slate-400"><LoaderCircle size={14} className="animate-spin" />{t('common.loading')}</div>}
        {treeQuery.isError && <p role="alert" className="p-3 text-xs leading-5 text-red-300">{treeQuery.error.message}</p>}
        {!treeQuery.isLoading && !tree.length && <p className="p-3 text-xs leading-5 text-slate-500">{t('editor.noFiles')}</p>}
        <div className="py-1">{tree.map((node) => <FileTreeItem key={node.path} node={node} selectedFile={selectedFile} draftPaths={draftPaths} onSelect={setSelectedFile} unsavedLabel={t('editor.unsaved')} />)}</div>
      </aside>

      <div className="flex min-h-0 min-w-0 flex-col">
        <div className="flex h-10 shrink-0 items-center justify-between gap-3 border-b border-slate-800 bg-slate-900 px-3">
          <span className="min-w-0 truncate font-mono text-xs text-slate-300">{selectedFile ?? t('editor.selectFile')}</span>
          {selectedFile && (
            <div className="flex shrink-0 items-center gap-2">
              <span className={`hidden text-xs sm:block ${dirty ? 'text-amber-300' : 'text-slate-500'}`}>{dirty ? t('editor.unsaved') : saveMessage}</span>
              <span className="sr-only" role="status" aria-live="polite">{dirty ? t('editor.unsaved') : saveMessage}</span>
              <button type="button" onClick={save} disabled={!dirty || conflict || readOnly || saveMutation.isPending} title={t('common.save')} className="flex h-7 w-7 items-center justify-center rounded-md bg-slate-800 text-slate-200 hover:bg-slate-700 disabled:text-slate-600">
                {saveMutation.isPending ? <LoaderCircle size={14} className="animate-spin" /> : saveMessage && !dirty ? <Check size={14} /> : <Save size={14} />}
              </button>
            </div>
          )}
        </div>

        {conflict && (
          <div role="alert" className="flex flex-col gap-3 border-b border-amber-700/50 bg-amber-950/70 px-3 py-3 text-xs text-amber-100 sm:flex-row sm:items-center">
            <span className="flex min-w-0 flex-1 items-start gap-2"><AlertTriangle size={15} className="shrink-0" />{t('editor.conflict')}</span>
            <div className="flex shrink-0 gap-2">
              <button type="button" onClick={() => void reloadLatest()} className="rounded-md border border-amber-700 px-2.5 py-1.5 hover:bg-amber-900">{t('editor.reload')}</button>
              <button type="button" onClick={() => void overwriteLatest()} className="rounded-md bg-amber-300 px-2.5 py-1.5 font-medium text-amber-950 hover:bg-amber-200">{t('editor.overwrite')}</button>
            </div>
          </div>
        )}
        {conflictResolutionError && <div role="alert" className="border-b border-red-800 bg-red-950 px-3 py-2 text-xs text-red-200">{conflictResolutionError}</div>}
        {saveMutation.isError && !conflict && <div role="alert" className="border-b border-red-800 bg-red-950 px-3 py-2 text-xs text-red-200">{saveMutation.error.message}</div>}

        <div className="min-h-0 flex-1">
          {!selectedFile && <div className="flex h-full items-center justify-center text-sm text-slate-500">{t('editor.selectFile')}</div>}
          {selectedFile && fileQuery.isLoading && <div className="flex h-full items-center justify-center text-sm text-slate-400"><LoaderCircle size={17} className="mr-2 animate-spin" />{t('common.loading')}</div>}
          {selectedFile && fileQuery.isError && <div role="alert" className="p-4 text-sm text-red-300">{fileQuery.error.message}</div>}
          {selectedFile && fileQuery.data && (
            <Editor
              height="100%"
              path={selectedFile}
              value={content}
              language={languageFromPath(selectedFile, fileQuery.data.language)}
              theme="vs-dark"
              onChange={(value) => {
                const nextContent = value ?? '';
                setContent(nextContent);
                if (selectedFile) {
                  setFileDraft(projectId, selectedFile, nextContent === savedContent && !conflict && !saveMutation.isPending ? null : {
                    content: nextContent,
                    baseContent: savedContent,
                    baseSha: loadedSha,
                  });
                }
                setSaveMessage('');
              }}
              options={{
                readOnly,
                minimap: { enabled: false },
                fontSize: 13,
                lineHeight: 21,
                scrollBeyondLastLine: false,
                automaticLayout: true,
                padding: { top: 12 },
              }}
            />
          )}
        </div>
      </div>
    </section>
  );
}

function FileTreeItem({ node, selectedFile, draftPaths, onSelect, unsavedLabel }: { node: TreeNode; selectedFile: string | null; draftPaths: Set<string>; onSelect: (path: string) => void; unsavedLabel: string }) {
  const [open, setOpen] = useState(true);
  if (node.type === 'file') {
    return (
      <button type="button" onClick={() => onSelect(node.path)} title={node.path} aria-label={draftPaths.has(node.path) ? `${node.name}: ${unsavedLabel}` : undefined} className={`flex h-7 w-full items-center gap-2 px-3 text-left text-xs ${selectedFile === node.path ? 'bg-slate-700 text-white' : 'text-slate-400 hover:bg-slate-800 hover:text-slate-200'}`}>
        <File size={13} className="shrink-0" /><span className="truncate">{node.name}</span>
        {draftPaths.has(node.path) && <span className="ml-auto h-1.5 w-1.5 shrink-0 rounded-full bg-amber-300" aria-hidden="true" />}
      </button>
    );
  }
  return (
    <div>
      <button type="button" onClick={() => setOpen((value) => !value)} className="flex h-7 w-full items-center gap-1.5 px-2 text-left text-xs text-slate-300 hover:bg-slate-800">
        <ChevronRight size={12} className={`shrink-0 transition-transform ${open ? 'rotate-90' : ''}`} />
        {open ? <FolderOpen size={13} className="shrink-0 text-sky-400" /> : <Folder size={13} className="shrink-0 text-sky-400" />}
        <span className="truncate">{node.name}</span>
      </button>
      {open && <div className="ml-3 border-l border-slate-800">{node.children.map((child) => <FileTreeItem key={child.path} node={child} selectedFile={selectedFile} draftPaths={draftPaths} onSelect={onSelect} unsavedLabel={unsavedLabel} />)}</div>}
    </div>
  );
}
