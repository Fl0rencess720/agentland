import { useState } from 'react';
import { motion } from 'motion/react';
import { 
  HelpCircle, Bell, Bot, Paperclip, Mic, Smile, Send, 
  Eye, Code2, Monitor, Tablet, Smartphone, Rocket, 
  Calendar, Sun, MoreHorizontal, ArrowLeft, Folder, User
} from 'lucide-react';
import CodeEditor from './CodeEditor';
import { useI18n } from '../i18n';
import LanguageSwitcher from './LanguageSwitcher';

export default function Workspace({ onBack, onProjects, onLogout }: { onBack: () => void, onProjects: () => void, onLogout: () => void }) {
  const [viewMode, setViewMode] = useState<'preview' | 'code'>('preview');
  const { t } = useI18n();

  return (
    <motion.div 
      initial={{ opacity: 0, y: 20, scale: 0.98 }}
      animate={{ opacity: 1, y: 0, scale: 1 }}
      transition={{ duration: 0.5, ease: [0.16, 1, 0.3, 1] }}
      className="min-h-screen flex flex-col bg-[#0B1120] text-white font-sans"
    >
      {/* Navbar */}
      <header className="flex items-center justify-between px-6 py-3 border-b border-slate-800/50 bg-[#0B1120] z-10 shrink-0">
        <div className="flex items-center gap-4">
          <button onClick={onBack} className="p-2 -ml-2 text-slate-400 hover:text-white transition-colors rounded-lg hover:bg-slate-800/50">
            <ArrowLeft size={20} />
          </button>
          <div className="flex items-center gap-2">
            <div className="w-6 h-6 text-blue-500">
              <svg viewBox="0 0 24 24" fill="currentColor">
                <path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/>
              </svg>
            </div>
            <span className="text-lg font-bold tracking-tight">AI App Gen</span>
          </div>
          <div className="w-px h-5 bg-slate-700 mx-2"></div>
          <span className="text-slate-400 text-sm">{t('workspace.projectUntitled')}</span>
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
            <button onClick={onLogout} className="w-8 h-8 rounded-full bg-slate-800 flex items-center justify-center text-slate-300 hover:text-white hover:bg-slate-700 transition-all">
              <User size={18} />
            </button>
          </div>
        </div>
      </header>

      <div className="flex-1 flex overflow-hidden">
        {/* Sidebar Chat */}
        <div className="w-[320px] flex flex-col border-r border-slate-800/50 bg-[#0B1120] shrink-0">
          {/* Chat Header */}
          <div className="px-5 py-4 border-b border-slate-800/50 flex items-center justify-between shrink-0">
            <div className="flex items-center gap-2">
              <Bot size={20} className="text-blue-500" />
              <span className="font-semibold">{t('workspace.codingAgent')}</span>
            </div>
            <span className="text-[10px] font-bold text-blue-500 bg-blue-500/10 px-2 py-0.5 rounded uppercase tracking-wider">{t('workspace.active')}</span>
          </div>

          {/* Chat Messages */}
          <div className="flex-1 overflow-y-auto p-5 flex flex-col gap-6">
            {/* Agent Message */}
            <div className="flex flex-col gap-1.5">
              <div className="flex items-center gap-2 text-xs text-slate-500">
                <div className="w-5 h-5 rounded-full bg-blue-600 flex items-center justify-center text-white">
                  <Bot size={12} />
                </div>
                <span>{t('workspace.agentTimeOld')}</span>
              </div>
              <div className="bg-[#1E293B] text-slate-200 text-sm p-3.5 rounded-2xl rounded-tl-sm leading-relaxed">
                {t('workspace.agentMsg1')}
              </div>
            </div>

            {/* User Message */}
            <div className="flex flex-col gap-1.5 items-end">
              <div className="text-xs text-slate-500">
                {t('workspace.youTime')}
              </div>
              <div className="bg-blue-600 text-white text-sm p-3.5 rounded-2xl rounded-tr-sm leading-relaxed max-w-[90%]">
                {t('workspace.userMsg1')}
              </div>
            </div>

            {/* Agent Message */}
            <div className="flex flex-col gap-1.5">
              <div className="flex items-center gap-2 text-xs text-slate-500">
                <div className="w-5 h-5 rounded-full bg-blue-600 flex items-center justify-center text-white">
                  <Bot size={12} />
                </div>
                <span>{t('workspace.agentTimeNow')}</span>
              </div>
              <div className="bg-[#1E293B] text-slate-200 text-sm p-3.5 rounded-2xl rounded-tl-sm leading-relaxed">
                {t('workspace.agentMsg2')}
              </div>
            </div>
          </div>

          {/* Chat Input */}
          <div className="p-4 shrink-0">
            <div className="bg-[#1E293B] rounded-xl p-3 flex flex-col gap-3">
              <textarea 
                placeholder={t('workspace.askPlaceholder')}
                className="w-full bg-transparent text-sm text-slate-200 placeholder:text-slate-500 resize-none outline-none min-h-[60px]"
              />
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-3 text-slate-400">
                  <button className="hover:text-slate-200 transition-colors"><Paperclip size={16} /></button>
                  <button className="hover:text-slate-200 transition-colors"><Mic size={16} /></button>
                  <button className="hover:text-slate-200 transition-colors"><Smile size={16} /></button>
                </div>
                <button className="bg-blue-600 hover:bg-blue-700 text-white px-4 py-1.5 rounded-lg text-sm font-medium flex items-center gap-2 transition-colors">
                  {t('workspace.send')} <Send size={14} />
                </button>
              </div>
            </div>
          </div>
        </div>

        {/* Main Content Area */}
        <div className="flex-1 flex flex-col min-w-0 bg-[#0B1120]">
          {/* Top Bar */}
          <div className="flex items-center justify-between px-6 py-2 border-b border-slate-800/50 shrink-0">
            <div className="flex items-center gap-6">
              <button 
                onClick={() => setViewMode('preview')}
                className={`flex items-center gap-2 font-medium relative py-3 ${viewMode === 'preview' ? 'text-blue-500' : 'text-slate-500 hover:text-slate-300 transition-colors'}`}
              >
                <Eye size={16} />
                <span>{t('nav.preview')}</span>
                {viewMode === 'preview' && <div className="absolute bottom-0 left-0 right-0 h-0.5 bg-blue-500"></div>}
              </button>
              <button 
                onClick={() => setViewMode('code')}
                className={`flex items-center gap-2 font-medium relative py-3 ${viewMode === 'code' ? 'text-blue-500' : 'text-slate-500 hover:text-slate-300 transition-colors'}`}
              >
                <Code2 size={16} />
                <span>{t('nav.code')}</span>
                {viewMode === 'code' && <div className="absolute bottom-0 left-0 right-0 h-0.5 bg-blue-500"></div>}
              </button>
            </div>
            <div className="flex items-center gap-4">
              <div className="flex items-center bg-slate-800/50 rounded-lg p-1">
                <button className="p-1.5 bg-slate-700 text-white rounded-md shadow-sm"><Monitor size={14} /></button>
                <button className="p-1.5 text-slate-500 hover:text-slate-300"><Tablet size={14} /></button>
                <button className="p-1.5 text-slate-500 hover:text-slate-300"><Smartphone size={14} /></button>
              </div>
              <button className="bg-blue-600 hover:bg-blue-700 text-white px-4 py-1.5 rounded-lg text-sm font-medium flex items-center gap-2 transition-colors">
                <Rocket size={14} /> {t('nav.deploy')}
              </button>
            </div>
          </div>

          {/* Preview / Code Area */}
          <div className="flex-1 p-6 overflow-hidden flex flex-col">
            {viewMode === 'preview' ? (
              <div className="flex-1 w-full max-w-5xl mx-auto bg-[#0B1120] rounded-xl overflow-hidden border border-slate-800 flex flex-col shadow-2xl">
                {/* Browser Header */}
                <div className="h-12 bg-[#111827] flex items-center px-4 gap-4 border-b border-slate-800 shrink-0">
                  <div className="flex gap-2">
                    <div className="w-3 h-3 rounded-full bg-red-500/80"></div>
                    <div className="w-3 h-3 rounded-full bg-yellow-500/80"></div>
                    <div className="w-3 h-3 rounded-full bg-green-500/80"></div>
                  </div>
                  <div className="flex-1 flex justify-center">
                    <div className="bg-[#0B1120] text-slate-400 text-xs px-32 py-1.5 rounded-md flex items-center gap-2 border border-slate-800">
                      <svg className="w-3 h-3" fill="currentColor" viewBox="0 0 24 24"><path d="M18 8h-1V6c0-2.76-2.24-5-5-5S7 3.24 7 6v2H6c-1.1 0-2 .9-2 2v10c0 1.1.9 2 2 2h12c1.1 0 2-.9 2-2V10c0-1.1-.9-2-2-2zM9 6c0-1.66 1.34-3 3-3s3 1.34 3 3v2H9V6zm9 14H6V10h12v10zm-6-3c1.1 0 2-.9 2-2s-.9-2-2-2-2 .9-2 2 .9 2 2 2z"/></svg>
                      localhost:3000/dashboard
                    </div>
                  </div>
                </div>
                
                {/* Browser Content - Analytics Dashboard */}
                <div className="flex-1 bg-[#0B1120] p-8 overflow-y-auto">
                  <div className="flex items-center justify-between mb-8">
                    <h1 className="text-2xl font-bold">{t('workspace.analyticsTitle')}</h1>
                    <div className="flex items-center gap-3">
                      <button className="flex items-center gap-2 bg-[#1E293B] border border-slate-700 px-4 py-2 rounded-lg text-sm text-slate-300 hover:bg-slate-800 transition-colors">
                        <Calendar size={16} className="text-blue-500" />
                        {t('workspace.dateRange')}
                      </button>
                      <button className="p-2 bg-[#1E293B] border border-slate-700 rounded-lg text-yellow-500 hover:bg-slate-800 transition-colors">
                        <Sun size={18} />
                      </button>
                    </div>
                  </div>

                  {/* Stats Cards */}
                  <div className="grid grid-cols-4 gap-4 mb-6">
                    <div className="bg-[#1E293B] p-5 rounded-xl border border-slate-800">
                      <div className="text-xs font-medium text-slate-400 mb-2 uppercase tracking-wider">{t('workspace.revenue')}</div>
                      <div className="flex items-end gap-3">
                        <div className="text-3xl font-bold">$42,500</div>
                        <div className="text-sm font-medium text-green-500 mb-1">+12%</div>
                      </div>
                    </div>
                    <div className="bg-[#1E293B] p-5 rounded-xl border border-slate-800">
                      <div className="text-xs font-medium text-slate-400 mb-2 uppercase tracking-wider">{t('workspace.users')}</div>
                      <div className="flex items-end gap-3">
                        <div className="text-3xl font-bold">8,432</div>
                        <div className="text-sm font-medium text-green-500 mb-1">+5%</div>
                      </div>
                    </div>
                    <div className="bg-[#1E293B] p-5 rounded-xl border border-slate-800">
                      <div className="text-xs font-medium text-slate-400 mb-2 uppercase tracking-wider">{t('workspace.avgSession')}</div>
                      <div className="flex items-end gap-3">
                        <div className="text-3xl font-bold">4m 32s</div>
                        <div className="text-sm font-medium text-red-500 mb-1">-2%</div>
                      </div>
                    </div>
                    <div className="bg-[#1E293B] p-5 rounded-xl border border-slate-800">
                      <div className="text-xs font-medium text-slate-400 mb-2 uppercase tracking-wider">{t('workspace.bounceRate')}</div>
                      <div className="flex items-end gap-3">
                        <div className="text-3xl font-bold">24.3%</div>
                        <div className="text-sm font-medium text-green-500 mb-1">-8%</div>
                      </div>
                    </div>
                  </div>

                  {/* Charts & Activity */}
                  <div className="grid grid-cols-3 gap-6">
                    <div className="col-span-2 bg-[#1E293B] p-6 rounded-xl border border-slate-800 flex flex-col">
                      <div className="flex items-center justify-between mb-8">
                        <h2 className="text-lg font-semibold">{t('workspace.growthTrends')}</h2>
                        <button className="text-slate-400 hover:text-slate-200"><MoreHorizontal size={20} /></button>
                      </div>
                      <div className="flex-1 flex items-end justify-between gap-4 pt-4">
                        {/* Mock Bar Chart */}
                        {[40, 60, 45, 80, 65, 95].map((height, i) => (
                          <div key={i} className="w-full flex flex-col justify-end gap-1 h-48">
                            <div className="w-full bg-blue-500/20 rounded-t-sm" style={{ height: `${100 - height}%` }}></div>
                            <div className="w-full bg-blue-600 rounded-t-sm" style={{ height: `${height}%` }}></div>
                          </div>
                        ))}
                      </div>
                    </div>
                    
                    <div className="bg-[#1E293B] p-6 rounded-xl border border-slate-800">
                      <h2 className="text-lg font-semibold mb-6">{t('workspace.recentActivity')}</h2>
                      <div className="flex flex-col gap-6">
                        <div className="flex gap-4">
                          <div className="w-8 h-8 rounded-full bg-slate-700 shrink-0"></div>
                          <div>
                            <div className="text-sm font-medium text-slate-200">{t('workspace.activity1')}</div>
                            <div className="text-xs text-slate-500 mt-1">{t('workspace.time1')}</div>
                          </div>
                        </div>
                        <div className="flex gap-4">
                          <div className="w-8 h-8 rounded-full bg-slate-700 shrink-0"></div>
                          <div>
                            <div className="text-sm font-medium text-slate-200">{t('workspace.activity2')}</div>
                            <div className="text-xs text-slate-500 mt-1">{t('workspace.time2')}</div>
                          </div>
                        </div>
                        <div className="flex gap-4">
                          <div className="w-8 h-8 rounded-full bg-slate-700 shrink-0"></div>
                          <div>
                            <div className="text-sm font-medium text-slate-200">{t('workspace.activity3')}</div>
                            <div className="text-xs text-slate-500 mt-1">{t('workspace.time3')}</div>
                          </div>
                        </div>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            ) : (
              <div className="flex-1 flex flex-col bg-[#1e1e1e] overflow-hidden rounded-xl border border-slate-800 shadow-2xl">
                <CodeEditor />
              </div>
            )}
          </div>
        </div>
      </div>
    </motion.div>
  );
}
