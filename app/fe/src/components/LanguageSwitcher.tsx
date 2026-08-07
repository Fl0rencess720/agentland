import { Languages } from 'lucide-react';
import { useI18n } from '../i18n';

export default function LanguageSwitcher() {
  const { locale, setLocale, t } = useI18n();

  return (
    <div
      className="flex items-center gap-1 rounded-md border border-slate-200 bg-white p-1"
      role="group"
      aria-label={t('lang.switcherAria')}
    >
      <Languages size={14} className="mx-1 text-slate-500" />
      <button
        className={`rounded px-2 py-1 text-xs font-medium transition-colors ${
          locale === 'en-US' ? 'bg-slate-900 text-white' : 'text-slate-600 hover:bg-slate-100'
        }`}
        aria-pressed={locale === 'en-US'}
        onClick={() => setLocale('en-US')}
      >
        {t('lang.en')}
      </button>
      <button
        className={`rounded px-2 py-1 text-xs font-medium transition-colors ${
          locale === 'zh-CN' ? 'bg-slate-900 text-white' : 'text-slate-600 hover:bg-slate-100'
        }`}
        aria-pressed={locale === 'zh-CN'}
        onClick={() => setLocale('zh-CN')}
      >
        {t('lang.zh')}
      </button>
    </div>
  );
}
