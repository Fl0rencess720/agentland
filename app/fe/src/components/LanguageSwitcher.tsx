import { Languages } from 'lucide-react';
import { useI18n } from '../i18n';

export default function LanguageSwitcher() {
  const { locale, setLocale, t } = useI18n();

  return (
    <div
      className="flex items-center gap-1 rounded-lg border border-slate-700 bg-[#111827] p-1"
      aria-label={t('lang.switcherAria')}
    >
      <Languages size={14} className="mx-1 text-slate-400" />
      <button
        className={`rounded px-2 py-1 text-xs font-medium transition-colors ${
          locale === 'en-US' ? 'bg-blue-600 text-white' : 'text-slate-300 hover:bg-slate-700'
        }`}
        onClick={() => setLocale('en-US')}
      >
        {t('lang.en')}
      </button>
      <button
        className={`rounded px-2 py-1 text-xs font-medium transition-colors ${
          locale === 'zh-CN' ? 'bg-blue-600 text-white' : 'text-slate-300 hover:bg-slate-700'
        }`}
        onClick={() => setLocale('zh-CN')}
      >
        {t('lang.zh')}
      </button>
    </div>
  );
}
