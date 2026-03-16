import { useRef, useState, type ChangeEvent } from 'react';
import { motion } from 'motion/react';
import {
  HelpCircle,
  Sparkles,
  Settings,
  Zap,
  Folder,
  Loader2,
  AlertCircle,
  ImagePlus,
  X,
} from 'lucide-react';
import { useI18n } from '../i18n';
import LanguageSwitcher from './LanguageSwitcher';
import DeepToggle from './DeepToggle';
import UserMenu from './UserMenu';
import type { FileUploadResult, GenerationAttachment, UserProfile } from '../api';

type DashboardProps = {
  onLogout: () => Promise<void> | void;
  onGenerate: (prompt: string, attachments: GenerationAttachment[]) => Promise<void> | void;
  onProjects: () => void;
  onUploadImage: (file: File) => Promise<FileUploadResult>;
  deepEnabled: boolean;
  onDeepEnabledChange: (next: boolean) => void;
  isGenerating: boolean;
  generationError: string | null;
  currentUser: UserProfile | null;
};

type UploadedImage = {
  file_id: string;
  name: string;
  mime_type: string;
};

export default function Dashboard({
  onLogout,
  onGenerate,
  onProjects,
  onUploadImage,
  deepEnabled,
  onDeepEnabledChange,
  isGenerating,
  generationError,
  currentUser,
}: DashboardProps) {
  const { t } = useI18n();
  const [prompt, setPrompt] = useState('');
  const [attachments, setAttachments] = useState<UploadedImage[]>([]);
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState<string | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);

  const handleGenerateClick = async () => {
    const normalizedPrompt = prompt.trim();
    if (!normalizedPrompt || isGenerating) {
      return;
    }
    await onGenerate(
      normalizedPrompt,
      attachments.map((attachment) => ({ file_id: attachment.file_id, name: attachment.name })),
    );
  };

  const handlePickImage = () => {
    if (uploading || isGenerating) {
      return;
    }
    fileInputRef.current?.click();
  };

  const handleImageChange = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (!file) {
      return;
    }

    setUploadError(null);
    setUploading(true);

    try {
      const uploaded = await onUploadImage(file);
      setAttachments((previous) => [
        ...previous,
        {
          file_id: uploaded.file_id,
          name: uploaded.name,
          mime_type: uploaded.mime_type,
        },
      ]);
    } catch (error) {
      setUploadError((error as Error).message || 'Failed to upload image.');
    } finally {
      setUploading(false);
    }
  };

  const removeAttachment = (fileId: string) => {
    setAttachments((previous) => previous.filter((item) => item.file_id !== fileId));
  };

  return (
    <motion.div
      initial={{ opacity: 0, y: 20 }}
      animate={{ opacity: 1, y: 0 }}
      exit={{ opacity: 0, scale: 0.98 }}
      transition={{ duration: 0.4, ease: [0.16, 1, 0.3, 1] }}
      className="min-h-screen flex flex-col bg-[#0B1120] text-white"
    >
      <header className="flex items-center justify-between px-6 py-3 border-b border-slate-800/50 bg-[#0B1120] shrink-0">
        <div className="flex items-center gap-3">
          <div className="w-6 h-6 text-blue-500">
            <svg viewBox="0 0 24 24" fill="currentColor">
              <path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5" />
            </svg>
          </div>
          <span className="text-lg font-bold tracking-tight">Agentland</span>
        </div>
        <div className="flex items-center gap-6">
          <LanguageSwitcher />
          <button
            onClick={onProjects}
            className="flex items-center gap-2 text-sm text-slate-400 hover:text-slate-200 transition-colors"
          >
            <Folder size={18} />
            <span>{t('nav.projects')}</span>
          </button>
          <button className="flex items-center gap-2 text-sm text-slate-400 hover:text-slate-200 transition-colors">
            <HelpCircle size={18} />
            <span>{t('nav.docs')}</span>
          </button>
          <UserMenu currentUser={currentUser} onLogout={onLogout} />
        </div>
      </header>

      <main className="flex-1 max-w-6xl w-full mx-auto p-6 flex flex-col gap-6">
        <div className="mt-4">
          <h1 className="text-3xl font-bold mb-2">{t('dashboard.title')}</h1>
          <p className="text-slate-400">{t('dashboard.subtitle')}</p>
        </div>

        <div className="bg-[#111827] border border-slate-800 rounded-2xl p-4 flex flex-col gap-4 shadow-lg">
          <div className="flex gap-4">
            <div className="w-10 h-10 rounded-xl bg-blue-500/10 flex items-center justify-center shrink-0">
              <Sparkles className="text-blue-500" size={20} />
            </div>
            <textarea
              className="w-full bg-transparent text-slate-300 placeholder:text-slate-600 resize-none outline-none py-2 min-h-[120px]"
              placeholder={t('dashboard.promptPlaceholder')}
              value={prompt}
              onChange={(event) => setPrompt(event.target.value)}
              disabled={isGenerating}
            />
          </div>

          {attachments.length > 0 && (
            <div className="flex flex-wrap gap-2 px-14">
              {attachments.map((attachment) => (
                <div
                  key={attachment.file_id}
                  className="inline-flex items-center gap-2 rounded-full border border-slate-700 bg-slate-800/70 px-3 py-1.5 text-xs text-slate-200"
                >
                  <ImagePlus size={14} className="text-blue-400" />
                  <span>{attachment.name}</span>
                  <button
                    onClick={() => removeAttachment(attachment.file_id)}
                    className="text-slate-400 hover:text-white"
                    aria-label={`Remove ${attachment.name}`}
                  >
                    <X size={12} />
                  </button>
                </div>
              ))}
            </div>
          )}

          <div className="flex flex-col gap-4 pt-2 border-t border-slate-800/50 md:flex-row md:items-center md:justify-between">
            <div className="flex flex-col gap-4 md:flex-row md:items-center">
              <div className="flex items-center gap-4 text-slate-500">
                <input
                  ref={fileInputRef}
                  type="file"
                  accept="image/*"
                  onChange={handleImageChange}
                  className="hidden"
                />
                <button onClick={handlePickImage} className="hover:text-slate-300 transition-colors" title="Upload image attachment">
                  {uploading ? <Loader2 size={18} className="animate-spin" /> : <ImagePlus size={18} />}
                </button>
                <button className="hover:text-slate-300 transition-colors">
                  <Settings size={18} />
                </button>
              </div>
              <DeepToggle
                checked={deepEnabled}
                onChange={onDeepEnabledChange}
                disabled={isGenerating || uploading}
              />
            </div>
            <button
              onClick={handleGenerateClick}
              disabled={isGenerating || uploading || !prompt.trim()}
              className="bg-blue-600 hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed text-white px-5 py-2.5 rounded-xl font-medium flex items-center gap-2 transition-colors shadow-lg shadow-blue-500/20"
            >
              {isGenerating ? (
                <>
                  <Loader2 size={16} className="animate-spin" />
                  <span>Generating...</span>
                </>
              ) : (
                <>
                  {t('dashboard.generate')} <Zap size={16} className="fill-current" />
                </>
              )}
            </button>
          </div>

          {uploadError && (
            <div className="rounded-lg px-3 py-2 text-sm border bg-amber-500/10 text-amber-200 border-amber-500/30">
              <span className="flex items-center gap-2">
                <AlertCircle size={14} /> {uploadError}
              </span>
            </div>
          )}

          {generationError && (
            <div className="rounded-lg px-3 py-2 text-sm border bg-red-500/10 text-red-300 border-red-500/30">
              <span className="flex items-center gap-2">
                <AlertCircle size={14} /> {generationError}
              </span>
            </div>
          )}
        </div>
      </main>

      <footer className="px-6 py-4 flex items-center justify-between text-xs text-slate-500 border-t border-slate-800">
        <div className="flex items-center gap-4">
          <div className="flex items-center gap-2">
            <div className="w-2 h-2 rounded-full bg-green-500"></div>
            <span>{t('dashboard.systemOnline')}</span>
          </div>
          <div className="w-px h-3 bg-slate-700"></div>
          <span>{t('dashboard.version')}</span>
        </div>
        <div className="flex items-center gap-6">
          <a href="#" className="hover:text-slate-300">
            {t('nav.privacy')}
          </a>
          <a href="#" className="hover:text-slate-300">
            {t('nav.terms')}
          </a>
          <div className="flex items-center gap-1 bg-slate-800/50 px-2 py-1 rounded border border-slate-700/50">
            <span className="font-mono">⌘ + K</span>
            <span className="ml-1">{t('dashboard.commandPalette')}</span>
          </div>
        </div>
      </footer>
    </motion.div>
  );
}
