export const queryKeys = {
  currentUser: ['auth', 'me'] as const,
  projects: (search: string) => ['projects', search] as const,
  project: (projectId: string) => ['project', projectId] as const,
  messages: (projectId: string) => ['project', projectId, 'messages'] as const,
  files: (projectId: string) => ['project', projectId, 'files', 'tree'] as const,
  fileContents: (projectId: string) => ['project', projectId, 'files', 'content'] as const,
  file: (projectId: string, path: string) => [...queryKeys.fileContents(projectId), path] as const,
  preview: (projectId: string) => ['project', projectId, 'preview'] as const,
  publications: (projectId: string) => ['project', projectId, 'publications'] as const,
  run: (runId: string) => ['run', runId] as const,
};
