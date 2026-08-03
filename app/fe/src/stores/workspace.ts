import { create } from 'zustand';
import { createJSONStorage, persist } from 'zustand/middleware';

export type WorkspaceTab = 'chat' | 'preview' | 'code';

export type FileDraft = {
  content: string;
  baseContent: string;
  baseSha: string;
};

type WorkspaceState = {
  mobileTab: WorkspaceTab;
  rightTab: Exclude<WorkspaceTab, 'chat'>;
  selectedFile: string | null;
  fileDrafts: Record<string, FileDraft>;
  setMobileTab: (tab: WorkspaceTab) => void;
  setRightTab: (tab: Exclude<WorkspaceTab, 'chat'>) => void;
  setSelectedFile: (path: string | null) => void;
  setFileDraft: (projectId: string, path: string, draft: FileDraft | null) => void;
  reset: () => void;
};

export function fileDraftKey(projectId: string, path: string) {
  return `${projectId}:${path}`;
}

export const WORKSPACE_DRAFTS_STORAGE_KEY = 'agentland.workspace-drafts';

export const useWorkspaceStore = create<WorkspaceState>()(persist((set) => ({
  mobileTab: 'chat',
  rightTab: 'preview',
  selectedFile: null,
  fileDrafts: {},
  setMobileTab: (mobileTab) => set({ mobileTab }),
  setRightTab: (rightTab) => set({ rightTab }),
  setSelectedFile: (selectedFile) => set({ selectedFile }),
  setFileDraft: (projectId, path, draft) => set((state) => {
    const key = fileDraftKey(projectId, path);
    if (draft) return { fileDrafts: { ...state.fileDrafts, [key]: draft } };
    const { [key]: _removed, ...fileDrafts } = state.fileDrafts;
    return { fileDrafts };
  }),
  reset: () => set({ mobileTab: 'chat', rightTab: 'preview', selectedFile: null }),
}), {
  name: WORKSPACE_DRAFTS_STORAGE_KEY,
  storage: createJSONStorage(() => sessionStorage),
  partialize: (state) => ({ fileDrafts: state.fileDrafts }),
}));
