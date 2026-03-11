import React, { useState } from 'react';
import Editor from '@monaco-editor/react';
import { ChevronRight, ChevronDown, Folder, FolderOpen, File, FileJson, FileCode2, X } from 'lucide-react';
import { useI18n } from '../i18n';

type FileNode = {
  id: string;
  name: string;
  type: 'file' | 'folder';
  language?: string;
  content?: string;
  children?: FileNode[];
};

const mockFileSystem: FileNode[] = [
  {
    id: 'src',
    name: 'src',
    type: 'folder',
    children: [
      {
        id: 'src/components',
        name: 'components',
        type: 'folder',
        children: [
          { 
            id: 'src/components/Dashboard.tsx', 
            name: 'Dashboard.tsx', 
            type: 'file', 
            language: 'typescript', 
            content: `import React, { useState } from 'react';\nimport { Eye, Code2 } from 'lucide-react';\n\nexport default function Dashboard() {\n  const [viewMode, setViewMode] = useState<'preview' | 'code'>('preview');\n\n  return (\n    <div className="p-4">\n      <h1 className="text-2xl font-bold">Dashboard</h1>\n      <p>Welcome to your new dashboard!</p>\n    </div>\n  );\n}` 
          },
          { 
            id: 'src/components/Login.tsx', 
            name: 'Login.tsx', 
            type: 'file', 
            language: 'typescript', 
            content: `export default function Login() {\n  return (\n    <div className="flex items-center justify-center min-h-screen">\n      <form className="p-8 bg-gray-800 rounded-xl">\n        <h2 className="text-xl mb-4 text-white">Sign In</h2>\n        <input type="email" placeholder="Email" className="block w-full mb-4 p-2 rounded" />\n        <input type="password" placeholder="Password" className="block w-full mb-4 p-2 rounded" />\n        <button className="w-full bg-blue-600 text-white p-2 rounded">Login</button>\n      </form>\n    </div>\n  );\n}` 
          }
        ]
      },
      { 
        id: 'src/App.tsx', 
        name: 'App.tsx', 
        type: 'file', 
        language: 'typescript', 
        content: `import { useState } from 'react';\nimport Login from './components/Login';\nimport Dashboard from './components/Dashboard';\n\nexport default function App() {\n  const [currentPage, setCurrentPage] = useState<'login' | 'dashboard'>('login');\n\n  return (\n    <div className="min-h-screen bg-[#0B1120] text-white">\n      {currentPage === 'login' ? (\n        <Login onLogin={() => setCurrentPage('dashboard')} />\n      ) : (\n        <Dashboard onLogout={() => setCurrentPage('login')} />\n      )}\n    </div>\n  );\n}` 
      },
      { 
        id: 'src/index.css', 
        name: 'index.css', 
        type: 'file', 
        language: 'css', 
        content: `@import "tailwindcss";\n\nbody {\n  margin: 0;\n  font-family: system-ui, sans-serif;\n}` 
      }
    ]
  },
  { 
    id: 'package.json', 
    name: 'package.json', 
    type: 'file', 
    language: 'json', 
    content: `{\n  "name": "ai-gencode-app",\n  "private": true,\n  "version": "0.0.0",\n  "type": "module",\n  "scripts": {\n    "dev": "vite",\n    "build": "tsc && vite build",\n    "lint": "eslint . --ext ts,tsx --report-unused-disable-directives --max-warnings 0",\n    "preview": "vite preview"\n  },\n  "dependencies": {\n    "react": "^18.2.0",\n    "react-dom": "^18.2.0",\n    "lucide-react": "^0.263.1"\n  }\n}` 
  },
  { 
    id: 'tailwind.config.js', 
    name: 'tailwind.config.js', 
    type: 'file', 
    language: 'javascript', 
    content: `/** @type {import('tailwindcss').Config} */\nexport default {\n  content: [\n    "./index.html",\n    "./src/**/*.{js,ts,jsx,tsx}",\n  ],\n  theme: {\n    extend: {},\n  },\n  plugins: [],\n}` 
  }
];

export default function CodeEditor() {
  const { t } = useI18n();
  const [expandedFolders, setExpandedFolders] = useState<Set<string>>(new Set(['src', 'src/components']));
  const [openFiles, setOpenFiles] = useState<FileNode[]>([mockFileSystem[0].children![0].children![0]]);
  const [activeFileId, setActiveFileId] = useState<string>('src/components/Dashboard.tsx');

  const toggleFolder = (id: string) => {
    const next = new Set(expandedFolders);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    setExpandedFolders(next);
  };

  const openFile = (file: FileNode) => {
    if (!openFiles.find(f => f.id === file.id)) {
      setOpenFiles([...openFiles, file]);
    }
    setActiveFileId(file.id);
  };

  const closeFile = (e: React.MouseEvent, id: string) => {
    e.stopPropagation();
    const newOpenFiles = openFiles.filter(f => f.id !== id);
    setOpenFiles(newOpenFiles);
    if (activeFileId === id) {
      setActiveFileId(newOpenFiles.length > 0 ? newOpenFiles[newOpenFiles.length - 1].id : '');
    }
  };

  const activeFile = openFiles.find(f => f.id === activeFileId);

  const renderTree = (nodes: FileNode[], depth = 0) => {
    return nodes.map(node => {
      const isExpanded = expandedFolders.has(node.id);
      const isActive = activeFileId === node.id;

      if (node.type === 'folder') {
        return (
          <div key={node.id}>
            <div
              className="flex items-center gap-1.5 py-1 px-2 cursor-pointer hover:bg-[#2a2d2e] text-sm text-[#cccccc] select-none"
              style={{ paddingLeft: `${depth * 12 + 8}px` }}
              onClick={() => toggleFolder(node.id)}
            >
              {isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
              {isExpanded ? <FolderOpen size={14} className="text-blue-400" /> : <Folder size={14} className="text-blue-400" />}
              <span>{node.name}</span>
            </div>
            {isExpanded && node.children && renderTree(node.children, depth + 1)}
          </div>
        );
      }

      return (
        <div
          key={node.id}
          className={`flex items-center gap-1.5 py-1 px-2 cursor-pointer text-sm select-none
            ${isActive ? 'bg-[#37373d] text-white' : 'hover:bg-[#2a2d2e] text-[#cccccc]'}`}
          style={{ paddingLeft: `${depth * 12 + 28}px` }}
          onClick={() => openFile(node)}
        >
          {node.name.endsWith('.json') ? <FileJson size={14} className="text-yellow-400" /> :
           node.name.endsWith('.tsx') || node.name.endsWith('.ts') ? <FileCode2 size={14} className="text-blue-400" /> :
           <File size={14} className="text-slate-400" />}
          <span>{node.name}</span>
        </div>
      );
    });
  };

  return (
    <div className="flex h-full w-full bg-[#1e1e1e] overflow-hidden">
      {/* Sidebar */}
      <div className="w-64 border-r border-[#2b2b2b] flex flex-col bg-[#252526]">
        <div className="px-4 py-3 text-xs font-semibold text-[#cccccc] tracking-wider">
          {t('editor.explorer')}
        </div>
        <div className="flex-1 overflow-y-auto py-2">
          {renderTree(mockFileSystem)}
        </div>
      </div>

      {/* Main Editor Area */}
      <div className="flex-1 flex flex-col min-w-0">
        {/* Tabs */}
        <div className="flex overflow-x-auto bg-[#252526] scrollbar-hide">
          {openFiles.map(file => (
            <div
              key={file.id}
              onClick={() => setActiveFileId(file.id)}
              className={`flex items-center gap-2 px-3 py-2 min-w-[120px] max-w-[200px] cursor-pointer border-r border-[#2b2b2b] group select-none
                ${activeFileId === file.id ? 'bg-[#1e1e1e] text-white border-t border-t-blue-500' : 'bg-[#2d2d2d] text-[#969696] hover:bg-[#2b2b2b]'}`}
            >
              {file.name.endsWith('.json') ? <FileJson size={14} className="text-yellow-400 shrink-0" /> :
               file.name.endsWith('.tsx') || file.name.endsWith('.ts') ? <FileCode2 size={14} className="text-blue-400 shrink-0" /> :
               <File size={14} className="text-slate-400 shrink-0" />}
              <span className="text-sm truncate flex-1">{file.name}</span>
              <button
                onClick={(e) => closeFile(e, file.id)}
                className={`p-0.5 rounded-md hover:bg-[#333333] ${activeFileId === file.id ? 'opacity-100' : 'opacity-0 group-hover:opacity-100'}`}
              >
                <X size={14} />
              </button>
            </div>
          ))}
        </div>

        {/* Editor */}
        <div className="flex-1 relative bg-[#1e1e1e]">
          {activeFile ? (
            <Editor
              height="100%"
              theme="vs-dark"
              language={activeFile.language || 'plaintext'}
              value={activeFile.content || ''}
              options={{
                readOnly: true,
                minimap: { enabled: false },
                fontSize: 14,
                wordWrap: 'on',
                scrollBeyondLastLine: false,
                padding: { top: 16 }
              }}
            />
          ) : (
            <div className="flex items-center justify-center h-full text-[#cccccc] text-lg">
              {t('editor.empty')}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
