import { memo, useCallback, useEffect, useMemo, useState } from 'react';
import type React from 'react';
import Editor from '@monaco-editor/react';
import {
  ChevronRight,
  ChevronDown,
  Folder,
  FolderOpen,
  File,
  FileJson,
  FileCode2,
  Download,
  X,
  Loader2,
  AlertCircle,
} from 'lucide-react';
import { useI18n } from '../i18n';
import type { FileContentResult, FileDownloadResult, FileTreeNode } from '../api';

type OpenFileTab = {
  path: string;
  name: string;
  language?: string;
  content?: string;
  loading: boolean;
  error?: string;
};

type CodeEditorProps = {
  tree: FileTreeNode[];
  loading: boolean;
  error: string | null;
  refreshSignal?: number;
  onOpenFile: (path: string) => Promise<FileContentResult>;
  onDownloadProject: () => Promise<FileDownloadResult>;
};

type TreeIndex = {
  paths: Set<string>;
  folderPaths: Set<string>;
};

function filename(path: string) {
  const parts = path.split('/').filter(Boolean);
  return parts.length ? parts[parts.length - 1] : path;
}

function guessLanguage(path: string) {
  if (path.endsWith('.tsx') || path.endsWith('.ts')) return 'typescript';
  if (path.endsWith('.jsx') || path.endsWith('.js')) return 'javascript';
  if (path.endsWith('.json')) return 'json';
  if (path.endsWith('.css')) return 'css';
  if (path.endsWith('.md')) return 'markdown';
  if (path.endsWith('.html')) return 'html';
  return 'plaintext';
}

function getDefaultExpanded(nodes: FileTreeNode[]) {
  const paths: string[] = [];
  const walk = (items: FileTreeNode[], depth: number) => {
    items.forEach((node) => {
      if (node.type === 'folder' && depth <= 1) {
        paths.push(node.path);
        if (node.children) {
          walk(node.children, depth + 1);
        }
      }
    });
  };
  walk(nodes, 0);
  return new Set(paths);
}

function buildTreeIndex(nodes: FileTreeNode[]): TreeIndex {
  const paths = new Set<string>();
  const folderPaths = new Set<string>();

  const walk = (items: FileTreeNode[]) => {
    items.forEach((node) => {
      paths.add(node.path);
      if (node.type === 'folder') {
        folderPaths.add(node.path);
        if (node.children) {
          walk(node.children);
        }
      }
    });
  };

  walk(nodes);
  return { paths, folderPaths };
}

const CodeEditor = memo(function CodeEditor({
  tree,
  loading,
  error,
  refreshSignal = 0,
  onOpenFile,
  onDownloadProject,
}: CodeEditorProps) {
  const { t } = useI18n();
  const [expandedFolders, setExpandedFolders] = useState<Set<string>>(new Set());
  const [openFiles, setOpenFiles] = useState<OpenFileTab[]>([]);
  const [activeFilePath, setActiveFilePath] = useState<string>('');
  const [downloadState, setDownloadState] = useState<{ loading: boolean; message: string | null; error: string | null }>({
    loading: false,
    message: null,
    error: null,
  });

  const treeIndex = useMemo(() => buildTreeIndex(tree), [tree]);

  useEffect(() => {
    const defaultExpanded = getDefaultExpanded(tree);

    setExpandedFolders((previous) => {
      const next = new Set<string>();
      previous.forEach((path) => {
        if (treeIndex.folderPaths.has(path)) {
          next.add(path);
        }
      });
      defaultExpanded.forEach((path) => {
        if (treeIndex.folderPaths.has(path)) {
          next.add(path);
        }
      });
      return next;
    });

    setOpenFiles((previous) => previous.filter((file) => treeIndex.paths.has(file.path)));
  }, [tree, treeIndex]);

  useEffect(() => {
    setActiveFilePath((previous) => {
      if (previous && openFiles.some((file) => file.path === previous)) {
        return previous;
      }
      return openFiles.length ? openFiles[openFiles.length - 1].path : '';
    });
  }, [openFiles]);

  const activeFile = useMemo(
    () => openFiles.find((file) => file.path === activeFilePath),
    [openFiles, activeFilePath],
  );

  const loadFile = useCallback(async (
    path: string,
    options?: {
      activate?: boolean;
      forceReload?: boolean;
      silent?: boolean;
    },
  ) => {
    const activate = options?.activate ?? true;
    const forceReload = options?.forceReload ?? false;
    const silent = options?.silent ?? false;
    const name = filename(path);
    let shouldFetch = forceReload;

    if (activate) {
      setActiveFilePath(path);
    }

    setOpenFiles((previous) => {
      const existing = previous.find((file) => file.path === path);

      if (!existing) {
        shouldFetch = true;
        return [
          ...previous,
          {
            path,
            name,
            loading: !silent,
            error: undefined,
          },
        ];
      }

      if (!forceReload && !existing.loading && (existing.content !== undefined || existing.error)) {
        return previous;
      }

      shouldFetch = true;
      return previous.map((file) => {
        if (file.path !== path) return file;
        return {
          ...file,
          loading: silent ? file.loading : true,
          error: undefined,
        };
      });
    });

    if (!shouldFetch) {
      return;
    }

    try {
      const content = await onOpenFile(path);
      setOpenFiles((previous) => {
        const existing = previous.find((file) => file.path === path);
        const nextFile: OpenFileTab = {
          path,
          name: existing?.name || name,
          loading: false,
          error: undefined,
          language: content.language ?? guessLanguage(path),
          content: content.content,
        };

        if (!existing) {
          return [...previous, nextFile];
        }

        return previous.map((file) => (file.path === path ? nextFile : file));
      });
    } catch (openError) {
      const message = (openError as Error).message;
      setOpenFiles((previous) =>
        previous.map((file) => {
          if (file.path !== path) return file;
          if (silent && file.content !== undefined) {
            return {
              ...file,
              loading: false,
            };
          }
          return {
            ...file,
            loading: false,
            error: message,
          };
        }),
      );
    }
  }, [onOpenFile]);

  useEffect(() => {
    if (!refreshSignal || !activeFilePath) {
      return;
    }

    void loadFile(activeFilePath, { activate: false, forceReload: true, silent: true });
  }, [activeFilePath, loadFile, refreshSignal]);

  const toggleFolder = (path: string) => {
    setExpandedFolders((previous) => {
      const next = new Set(previous);
      if (next.has(path)) {
        next.delete(path);
      } else {
        next.add(path);
      }
      return next;
    });
  };

  const openFile = async (node: FileTreeNode) => {
    if (node.type !== 'file') {
      return;
    }

    await loadFile(node.path, { activate: true, forceReload: false, silent: false });
  };

  const closeFile = (event: React.MouseEvent, path: string) => {
    event.stopPropagation();
    setOpenFiles((previous) => previous.filter((file) => file.path !== path));
  };

  const downloadProjectArchive = async () => {
    if (downloadState.loading) {
      return;
    }

    setDownloadState({ loading: true, message: null, error: null });
    try {
      const result = await onDownloadProject();
      const fileName = result.file_name || 'project.zip';
      setDownloadState({ loading: false, message: t('editor.downloaded', { fileName }), error: null });
    } catch (downloadError) {
      setDownloadState({ loading: false, message: null, error: (downloadError as Error).message });
    }
  };

  const renderTree = (nodes: FileTreeNode[], depth = 0) => {
    return nodes.map((node) => {
      const isExpanded = expandedFolders.has(node.path);
      const isActive = activeFilePath === node.path;

      if (node.type === 'folder') {
        return (
          <div key={node.path}>
            <div
              className="flex items-center gap-1.5 py-1 px-2 cursor-pointer hover:bg-[#2a2d2e] text-sm text-[#cccccc] select-none"
              style={{ paddingLeft: `${depth * 12 + 8}px` }}
              onClick={() => toggleFolder(node.path)}
            >
              {isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
              {isExpanded ? (
                <FolderOpen size={14} className="text-blue-400" />
              ) : (
                <Folder size={14} className="text-blue-400" />
              )}
              <span>{node.name}</span>
            </div>
            {isExpanded && node.children && renderTree(node.children, depth + 1)}
          </div>
        );
      }

      return (
        <div
          key={node.path}
          className={`flex items-center gap-1.5 py-1 px-2 cursor-pointer text-sm select-none
            ${isActive ? 'bg-[#37373d] text-white' : 'hover:bg-[#2a2d2e] text-[#cccccc]'}`}
          style={{ paddingLeft: `${depth * 12 + 28}px` }}
          onClick={() => void openFile(node)}
        >
          {node.name.endsWith('.json') ? (
            <FileJson size={14} className="text-yellow-400" />
          ) : node.name.endsWith('.tsx') || node.name.endsWith('.ts') ? (
            <FileCode2 size={14} className="text-blue-400" />
          ) : (
            <File size={14} className="text-slate-400" />
          )}
          <span>{node.name}</span>
        </div>
      );
    });
  };

  return (
    <div className="flex h-full w-full bg-[#1e1e1e] overflow-hidden">
      <div className="w-64 border-r border-[#2b2b2b] flex flex-col bg-[#252526]">
        <div className="px-4 py-3 text-xs font-semibold text-[#cccccc] tracking-wider">
          {t('editor.explorer')}
        </div>
        <div className="flex-1 overflow-y-auto py-2">
          {loading ? (
            <div className="h-full flex items-center justify-center text-slate-400 gap-2 text-sm">
              <Loader2 size={16} className="animate-spin" /> {t('editor.loadingFiles')}
            </div>
          ) : error ? (
            <div className="h-full px-3 flex items-center justify-center text-red-300 gap-2 text-sm">
              <AlertCircle size={14} /> {error}
            </div>
          ) : tree.length === 0 ? (
            <div className="h-full flex items-center justify-center text-slate-500 text-sm">No files found.</div>
          ) : (
            renderTree(tree)
          )}
        </div>
      </div>

      <div className="flex-1 flex flex-col min-w-0">
        <div className="flex items-center justify-between gap-3 bg-[#252526] border-b border-[#2b2b2b]">
          <div className="flex overflow-x-auto scrollbar-hide flex-1 min-w-0">
            {openFiles.map((file) => (
              <div
                key={file.path}
                onClick={() => {
                  void loadFile(file.path, { activate: true, forceReload: true, silent: true });
                }}
                className={`flex items-center gap-2 px-3 py-2 min-w-[140px] max-w-[260px] cursor-pointer border-r border-[#2b2b2b] group select-none
                  ${
                    activeFilePath === file.path
                      ? 'bg-[#1e1e1e] text-white border-t border-t-blue-500'
                      : 'bg-[#2d2d2d] text-[#969696] hover:bg-[#2b2b2b]'
                  }`}
              >
                {file.name.endsWith('.json') ? (
                  <FileJson size={14} className="text-yellow-400 shrink-0" />
                ) : file.name.endsWith('.tsx') || file.name.endsWith('.ts') ? (
                  <FileCode2 size={14} className="text-blue-400 shrink-0" />
                ) : (
                  <File size={14} className="text-slate-400 shrink-0" />
                )}
                <span className="text-sm truncate flex-1">{file.name}</span>
                <button
                  onClick={(event) => closeFile(event, file.path)}
                  className={`p-0.5 rounded-md hover:bg-[#333333] ${
                    activeFilePath === file.path ? 'opacity-100' : 'opacity-0 group-hover:opacity-100'
                  }`}
                >
                  <X size={14} />
                </button>
              </div>
            ))}
          </div>

          <div className="shrink-0 px-3">
            <button
              onClick={downloadProjectArchive}
              disabled={downloadState.loading}
              className="inline-flex items-center gap-2 rounded-md border border-slate-700 bg-[#1f2937] px-3 py-1.5 text-xs text-slate-200 hover:bg-slate-700 disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {downloadState.loading ? <Loader2 size={14} className="animate-spin" /> : <Download size={14} />}
              <span>{t('editor.downloadProject')}</span>
            </button>
          </div>
        </div>

        <div className="flex-1 relative bg-[#1e1e1e]">
          {(downloadState.message || downloadState.error) && (
            <div className={`absolute right-4 top-4 z-10 rounded-md px-3 py-2 text-xs ${downloadState.error ? 'bg-red-500/15 text-red-200' : 'bg-emerald-500/15 text-emerald-200'}`}>
              {downloadState.error || downloadState.message}
            </div>
          )}
          {!activeFile ? (
            <div className="flex items-center justify-center h-full text-[#cccccc] text-lg">{t('editor.empty')}</div>
          ) : activeFile.loading && activeFile.content === undefined ? (
            <div className="flex items-center justify-center h-full text-slate-300 text-sm gap-2">
              <Loader2 size={16} className="animate-spin" /> Loading {activeFile.name}...
            </div>
          ) : activeFile.error ? (
            <div className="flex items-center justify-center h-full text-red-300 text-sm gap-2">
              <AlertCircle size={14} /> {activeFile.error}
            </div>
          ) : (
            <Editor
              height="100%"
              theme="vs-dark"
              language={activeFile.language || guessLanguage(activeFile.path)}
              value={activeFile.content || ''}
              options={{
                readOnly: true,
                minimap: { enabled: false },
                fontSize: 14,
                wordWrap: 'on',
                scrollBeyondLastLine: false,
                padding: { top: 16 },
              }}
            />
          )}
        </div>
      </div>
    </div>
  );
});

export default CodeEditor;
