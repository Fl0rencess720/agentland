import { createContext, useContext, useMemo, useState, type ReactNode } from 'react';

export type Locale = 'en-US' | 'zh-CN';

type TranslateParams = Record<string, string | number>;
type I18nContextValue = {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: (key: string, params?: TranslateParams) => string;
};

const STORAGE_KEY = 'fee.locale';

const messages: Record<Locale, Record<string, string>> = {
  'en-US': {
    'lang.switcherAria': 'Switch language',
    'lang.en': 'EN',
    'lang.zh': '中文',
    'nav.projects': 'Projects',
    'nav.docs': 'Docs',
    'nav.share': 'Share',
    'nav.publish': 'Publish',
    'nav.preview': 'Preview',
    'nav.code': 'Code',
    'nav.deploy': 'Deploy',
    'nav.privacy': 'Privacy Policy',
    'nav.terms': 'Terms of Service',
    'common.and': 'and',
    'login.authType': 'GitHub Authentication',
    'login.welcome': 'Welcome Back',
    'login.subtitle': 'Sign in with GitHub to access your AI workspace',
    'login.githubCta': 'Continue with GitHub',
    'login.onlyGithub': 'Only GitHub login is supported.',
    'login.agreementPrefix': 'By logging in, you agree to our',
    'login.footer': '© 2026 Agentland. All rights reserved. Powered by Advanced Neural Engines.',
    'dashboard.title': 'Build something incredible',
    'dashboard.subtitle': 'Describe the application you want to build and let AI handle the engineering.',
    'dashboard.promptPlaceholder': 'for example, Create a sleek SaaS dashboard with dark mode support, real-time charts, and side navigation for users and subscriptions...',
    'dashboard.generate': 'Generate App',
    'dashboard.systemOnline': 'System Online',
    'dashboard.version': 'Version 2.4.0-pro',
    'dashboard.commandPalette': 'for Command Palette',
    'workspace.projectUntitled': 'Untitled Project',
    'workspace.codingAgent': 'Coding Agent',
    'workspace.active': 'Active',
    'workspace.agentTimeOld': 'Agent • 2m ago',
    'workspace.youTime': 'You • 1m ago',
    'workspace.agentTimeNow': 'Agent • Just now',
    'workspace.agentMsg1': "I've initialized the React components for your dashboard. I used Tailwind CSS for styling. How would you like to proceed with data fetching?",
    'workspace.userMsg1': 'Can you add a dark mode toggle to the header and show me the code?',
    'workspace.agentMsg2': "Certainly. I've updated the Header.tsx component and added a theme context provider. You can check the Code tab to see the changes.",
    'workspace.askPlaceholder': 'Ask the Coding Agent...',
    'workspace.send': 'Send',
    'workspace.conversations': 'Conversations',
    'workspace.noConversations': 'No conversations',
    'workspace.analyticsTitle': 'Analytics Dashboard',
    'workspace.dateRange': 'Oct 2023 - Nov 2023',
    'workspace.revenue': 'Revenue',
    'workspace.users': 'Users',
    'workspace.avgSession': 'Avg. Session',
    'workspace.bounceRate': 'Bounce Rate',
    'workspace.growthTrends': 'Growth Trends',
    'workspace.recentActivity': 'Recent Activity',
    'workspace.activity1': 'New user registered',
    'workspace.activity2': 'Payment received',
    'workspace.activity3': 'Server updated',
    'workspace.time1': '2 minutes ago',
    'workspace.time2': '15 minutes ago',
    'workspace.time3': '1 hour ago',
    'projects.newApp': 'New App',
    'projects.all': 'All Projects',
    'projects.recent': 'Recent',
    'projects.shared': 'Shared',
    'projects.usage': 'Usage',
    'projects.usageCount': '{used} of {limit} projects used',
    'projects.title': 'My Projects',
    'projects.subtitle': 'Manage and build your AI-generated applications',
    'projects.searchPlaceholder': 'Search projects by name...',
    'projects.filter': 'Filter',
    'projects.sort': 'Sort',
    'projects.created': 'Created {date}',
    'projects.openEditor': 'Open Editor',
    'projects.createNew': 'Create New App',
    'projects.createNewDesc': 'Start a new project from scratch',
    'projects.name1': 'SaaS Dashboard',
    'projects.name2': 'E-commerce Site',
    'projects.name3': 'AI Chatbot UI',
    'projects.name4': 'Portfolio Page',
    'projects.name5': 'Marketing Analytics',
    'status.deployed': 'DEPLOYED',
    'status.draft': 'DRAFT',
    'status.building': 'BUILDING',
    'editor.explorer': 'EXPLORER',
    'editor.empty': 'Select a file to view its code',
    'editor.downloadProject': 'Download Project',
    'editor.downloaded': 'Downloaded {fileName}',
  },
  'zh-CN': {
    'lang.switcherAria': '切换语言',
    'lang.en': 'EN',
    'lang.zh': '中文',
    'nav.projects': '项目',
    'nav.docs': '文档',
    'nav.share': '分享',
    'nav.publish': '发布',
    'nav.preview': '预览',
    'nav.code': '代码',
    'nav.deploy': '部署',
    'nav.privacy': '隐私政策',
    'nav.terms': '服务条款',
    'common.and': '和',
    'login.authType': 'GitHub 认证',
    'login.welcome': '欢迎回来',
    'login.subtitle': '使用 GitHub 登录以访问你的 AI 工作区',
    'login.githubCta': '使用 GitHub 继续',
    'login.onlyGithub': '当前仅支持 GitHub 登录。',
    'login.agreementPrefix': '登录即表示你同意我们的',
    'login.footer': '© 2026 Agentland。保留所有权利。由 Advanced Neural Engines 提供支持。',
    'dashboard.title': '开始构建你的应用',
    'dashboard.subtitle': '描述你要构建的应用，让 AI 完成工程实现。',
    'dashboard.promptPlaceholder': '例如：创建一个支持暗黑模式的 SaaS 仪表盘，包含实时图表与用户/订阅侧边导航...',
    'dashboard.generate': '生成应用',
    'dashboard.systemOnline': '系统在线',
    'dashboard.version': '版本 2.4.0-pro',
    'dashboard.commandPalette': '打开命令面板',
    'workspace.projectUntitled': '未命名项目',
    'workspace.codingAgent': '编码助手',
    'workspace.active': '运行中',
    'workspace.agentTimeOld': '助手 • 2 分钟前',
    'workspace.youTime': '你 • 1 分钟前',
    'workspace.agentTimeNow': '助手 • 刚刚',
    'workspace.agentMsg1': '我已经初始化了你仪表盘的 React 组件，并使用 Tailwind CSS 完成样式。接下来你希望如何处理数据请求？',
    'workspace.userMsg1': '可以在头部加一个暗黑模式切换，并把代码给我看吗？',
    'workspace.agentMsg2': '可以。我已经更新了 Header.tsx，并添加了主题上下文。你可以在代码标签页查看修改。',
    'workspace.askPlaceholder': '向编码助手提问...',
    'workspace.send': '发送',
    'workspace.conversations': '会话',
    'workspace.noConversations': '暂无会话',
    'workspace.analyticsTitle': '分析仪表盘',
    'workspace.dateRange': '2023年10月 - 2023年11月',
    'workspace.revenue': '营收',
    'workspace.users': '用户',
    'workspace.avgSession': '平均会话',
    'workspace.bounceRate': '跳出率',
    'workspace.growthTrends': '增长趋势',
    'workspace.recentActivity': '最近活动',
    'workspace.activity1': '新用户注册',
    'workspace.activity2': '收到付款',
    'workspace.activity3': '服务器更新',
    'workspace.time1': '2 分钟前',
    'workspace.time2': '15 分钟前',
    'workspace.time3': '1 小时前',
    'projects.newApp': '新建应用',
    'projects.all': '全部项目',
    'projects.recent': '最近',
    'projects.shared': '共享',
    'projects.usage': '用量',
    'projects.usageCount': '已使用 {used}/{limit} 个项目',
    'projects.title': '我的项目',
    'projects.subtitle': '管理并构建你的 AI 生成应用',
    'projects.searchPlaceholder': '按名称搜索项目...',
    'projects.filter': '筛选',
    'projects.sort': '排序',
    'projects.created': '创建于 {date}',
    'projects.openEditor': '打开编辑器',
    'projects.createNew': '创建新应用',
    'projects.createNewDesc': '从零开始创建一个新项目',
    'projects.name1': 'SaaS 仪表盘',
    'projects.name2': '电商站点',
    'projects.name3': 'AI 聊天 UI',
    'projects.name4': '作品集页面',
    'projects.name5': '营销分析',
    'status.deployed': '已部署',
    'status.draft': '草稿',
    'status.building': '构建中',
    'editor.explorer': '资源管理器',
    'editor.empty': '选择文件以查看代码',
    'editor.downloadProject': '下载项目',
    'editor.downloaded': '已下载 {fileName}',
  },
};

function detectInitialLocale(): Locale {
  const saved = localStorage.getItem(STORAGE_KEY);
  if (saved === 'en-US' || saved === 'zh-CN') {
    return saved;
  }
  return navigator.language.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en-US';
}

function formatMessage(template: string, params?: TranslateParams) {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (_, key: string) => {
    const value = params[key];
    return value === undefined ? `{${key}}` : String(value);
  });
}

const I18nContext = createContext<I18nContextValue | null>(null);

export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(detectInitialLocale);

  const setLocale = (next: Locale) => {
    setLocaleState(next);
    localStorage.setItem(STORAGE_KEY, next);
  };

  const value = useMemo<I18nContextValue>(() => {
    const t = (key: string, params?: TranslateParams) => {
      const msg = messages[locale][key] ?? messages['en-US'][key] ?? key;
      return formatMessage(msg, params);
    };
    return { locale, setLocale, t };
  }, [locale]);

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n() {
  const ctx = useContext(I18nContext);
  if (!ctx) {
    throw new Error('useI18n must be used inside I18nProvider');
  }
  return ctx;
}
