import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';

export type Locale = 'en-US' | 'zh-CN';
type TranslateParams = Record<string, string | number>;
type I18nContextValue = {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: (key: string, params?: TranslateParams) => string;
};

const STORAGE_KEY = 'agentland.locale';

const messages: Record<Locale, Record<string, string>> = {
  'en-US': {
    'lang.switcherAria': 'Switch language',
    'lang.en': 'EN',
    'lang.zh': '中文',
    'common.loading': 'Loading...',
    'common.retry': 'Retry',
    'common.cancel': 'Cancel',
    'common.save': 'Save',
    'common.delete': 'Delete',
    'common.create': 'Create',
    'common.back': 'Back',
    'account.logout': 'Log out',
    'account.fallback': 'Account',
    'login.title': 'Sign in to Agentland',
    'login.subtitle': 'Continue to your projects.',
    'login.github': 'Continue with GitHub',
    'login.authenticating': 'Signing in...',
    'login.error': 'Sign-in failed. Try again.',
    'projects.title': 'Projects',
    'projects.subtitle': 'Your AI application projects.',
    'projects.new': 'New project',
    'projects.name': 'Project name',
    'projects.namePlaceholder': 'Untitled project',
    'projects.search': 'Search projects',
    'projects.empty': 'No projects yet',
    'projects.emptySearch': 'No projects match this search.',
    'projects.updated': 'Updated {date}',
    'projects.open': 'Open project',
    'projects.deleteConfirm': 'Delete this project?',
    'workspace.chat': 'Chat',
    'workspace.preview': 'Preview',
    'workspace.code': 'Code',
    'workspace.publish': 'Publish',
    'workspace.agent': 'Agent',
    'workspace.you': 'You',
    'workspace.placeholder': 'Describe the change you want...',
    'workspace.send': 'Send',
    'workspace.stop': 'Stop run',
    'workspace.stopping': 'Stopping...',
    'workspace.empty': 'No messages yet.',
    'workspace.loadEarlier': 'Load earlier messages',
    'workspace.running': 'Agent is working',
    'workspace.failed': 'The run failed',
    'workspace.cancelled': 'The run was cancelled',
    'workspace.connectionError': 'The live event stream disconnected.',
    'workspace.toolRunning': 'Running',
    'workspace.toolDone': 'Completed',
    'workspace.toolFailed': 'Failed',
    'workspace.runtimeExpired': 'This runtime has expired. Chat history remains available; create a new project to continue.',
    'workspace.backToProjects': 'Back to projects',
    'workspace.runtimeUnavailable': 'The runtime will be created when you send the first message. Files and preview are unavailable until then.',
    'editor.files': 'Files',
    'editor.selectFile': 'Select a file to inspect it.',
    'editor.noFiles': 'No files found.',
    'editor.runtimeUnavailable': 'Files are available while the project runtime is active.',
    'editor.saved': 'Saved',
    'editor.unsaved': 'Unsaved changes',
    'editor.conflict': 'This file changed on the server. Your local draft is preserved until you reload it or overwrite the latest version.',
    'editor.reload': 'Reload server version',
    'editor.overwrite': 'Overwrite latest',
    'preview.port': 'Port',
    'preview.start': 'Start preview',
    'preview.refresh': 'Refresh',
    'preview.viewport': 'Preview viewport',
    'preview.desktop': 'Desktop',
    'preview.tablet': 'Tablet',
    'preview.mobile': 'Mobile',
    'preview.empty': 'Preview is not running.',
    'preview.starting': 'Preview is starting...',
    'preview.failed': 'Preview failed to start.',
    'preview.invalidOrigin': 'The preview URL must use a valid origin isolated from Agentland.',
    'preview.runtimeUnavailable': 'Preview is available while the project runtime is active.',
    'publish.title': 'Deploy application',
    'publish.context': 'Build context',
    'publish.dockerfile': 'Dockerfile',
    'publish.start': 'Deploy',
    'publish.preparing': 'Preparing Dockerfile',
    'publish.queued': 'Queued',
    'publish.running': 'Building and deploying',
    'publish.completed': 'Deployed',
    'publish.failed': 'Failed',
    'publish.cancelled': 'Cancelled',
    'publish.empty': 'No deployments yet.',
    'publish.image': 'Immutable image',
    'publish.application': 'Application URL',
    'publish.logs': 'Build and deployment log',
    'publish.history': 'Deployment history',
    'publish.runtimeUnavailable': 'Deployment requires an active runtime and no active Agent run.',
  },
  'zh-CN': {
    'lang.switcherAria': '切换语言',
    'lang.en': 'EN',
    'lang.zh': '中文',
    'common.loading': '加载中...',
    'common.retry': '重试',
    'common.cancel': '取消',
    'common.save': '保存',
    'common.delete': '删除',
    'common.create': '创建',
    'common.back': '返回',
    'account.logout': '退出登录',
    'account.fallback': '账户',
    'login.title': '登录 Agentland',
    'login.subtitle': '继续访问你的项目。',
    'login.github': '使用 GitHub 继续',
    'login.authenticating': '正在登录...',
    'login.error': '登录失败，请重试。',
    'projects.title': '项目',
    'projects.subtitle': '你的 AI 应用项目。',
    'projects.new': '新建项目',
    'projects.name': '项目名称',
    'projects.namePlaceholder': '未命名项目',
    'projects.search': '搜索项目',
    'projects.empty': '还没有项目',
    'projects.emptySearch': '没有符合搜索条件的项目。',
    'projects.updated': '更新于 {date}',
    'projects.open': '打开项目',
    'projects.deleteConfirm': '确认删除这个项目？',
    'workspace.chat': '对话',
    'workspace.preview': '预览',
    'workspace.code': '代码',
    'workspace.publish': '发布',
    'workspace.agent': 'Agent',
    'workspace.you': '你',
    'workspace.placeholder': '描述你希望实现的修改...',
    'workspace.send': '发送',
    'workspace.stop': '停止运行',
    'workspace.stopping': '正在停止...',
    'workspace.empty': '暂无消息。',
    'workspace.loadEarlier': '加载更早消息',
    'workspace.running': 'Agent 正在处理',
    'workspace.failed': '本次运行失败',
    'workspace.cancelled': '本次运行已取消',
    'workspace.connectionError': '实时事件连接已经断开。',
    'workspace.toolRunning': '执行中',
    'workspace.toolDone': '已完成',
    'workspace.toolFailed': '失败',
    'workspace.runtimeExpired': '运行环境已经过期。聊天历史仍可查看，请创建新项目继续。',
    'workspace.backToProjects': '返回项目列表',
    'workspace.runtimeUnavailable': '发送第一条消息后将创建运行环境，在此之前文件和预览暂不可用。',
    'editor.files': '文件',
    'editor.selectFile': '选择文件以查看内容。',
    'editor.noFiles': '没有找到文件。',
    'editor.runtimeUnavailable': '项目运行环境启动后才能访问文件。',
    'editor.saved': '已保存',
    'editor.unsaved': '有未保存修改',
    'editor.conflict': '服务端文件已经变化，本地草稿会继续保留。请选择加载服务端版本，或用本地草稿覆盖最新版本。',
    'editor.reload': '加载服务端版本',
    'editor.overwrite': '覆盖最新版本',
    'preview.port': '端口',
    'preview.start': '启动预览',
    'preview.refresh': '刷新',
    'preview.viewport': '预览设备尺寸',
    'preview.desktop': '桌面端',
    'preview.tablet': '平板',
    'preview.mobile': '手机',
    'preview.empty': '预览尚未启动。',
    'preview.starting': '正在启动预览...',
    'preview.failed': '预览启动失败。',
    'preview.invalidOrigin': '预览地址必须使用与 Agentland 隔离的有效来源。',
    'preview.runtimeUnavailable': '项目运行环境启动后才能使用预览。',
    'publish.title': '部署应用',
    'publish.context': '构建目录',
    'publish.dockerfile': 'Dockerfile',
    'publish.start': '部署',
    'publish.preparing': '正在准备 Dockerfile',
    'publish.queued': '等待中',
    'publish.running': '正在构建并部署',
    'publish.completed': '已部署',
    'publish.failed': '失败',
    'publish.cancelled': '已取消',
    'publish.empty': '还没有部署记录。',
    'publish.image': '不可变镜像',
    'publish.application': '应用地址',
    'publish.logs': '构建与部署日志',
    'publish.history': '部署记录',
    'publish.runtimeUnavailable': '部署需要有效运行环境，且 Agent 当前没有运行任务。',
  },
};

function detectInitialLocale(): Locale {
  if (typeof window === 'undefined') return 'zh-CN';
  const stored = localStorage.getItem(STORAGE_KEY);
  if (stored === 'en-US' || stored === 'zh-CN') return stored;
  return navigator.language.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en-US';
}

function formatMessage(template: string, params?: TranslateParams) {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (_, key: string) => String(params[key] ?? `{${key}}`));
}

const I18nContext = createContext<I18nContextValue | null>(null);

export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(detectInitialLocale);

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  const value = useMemo<I18nContextValue>(() => ({
    locale,
    setLocale: (next) => {
      localStorage.setItem(STORAGE_KEY, next);
      setLocaleState(next);
    },
    t: (key, params) => formatMessage(messages[locale][key] ?? messages['en-US'][key] ?? key, params),
  }), [locale]);

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n() {
  const context = useContext(I18nContext);
  if (!context) throw new Error('useI18n must be used inside I18nProvider');
  return context;
}
