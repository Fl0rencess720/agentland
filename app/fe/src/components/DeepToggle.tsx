import { BrainCircuit } from 'lucide-react';
import { useI18n } from '../i18n';

type DeepToggleProps = {
  checked: boolean;
  onChange: (next: boolean) => void;
  disabled?: boolean;
  className?: string;
};

export default function DeepToggle({
  checked,
  onChange,
  disabled = false,
  className = '',
}: DeepToggleProps) {
  const { t } = useI18n();

  return (
    <div className={`flex items-center gap-3 ${className}`.trim()}>
      <div className="flex items-center gap-3 min-w-0">
        <div
          className={`flex h-9 w-9 shrink-0 items-center justify-center rounded-xl border transition-colors ${
            checked
              ? 'border-blue-500/40 bg-blue-500/12 text-blue-300'
              : 'border-slate-700 bg-slate-900/80 text-slate-500'
          }`}
        >
          <BrainCircuit size={16} />
        </div>
        <div className="min-w-0">
          <div className="text-sm font-medium text-slate-200">{t('deep.label')}</div>
          <div className="text-[11px] text-slate-500 leading-4">{t('deep.description')}</div>
        </div>
      </div>

      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={t('deep.toggleAria')}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        className={`relative inline-flex h-7 w-12 shrink-0 items-center rounded-full border transition-all ${
          checked
            ? 'border-blue-500/50 bg-blue-500/90 shadow-[0_0_24px_rgba(59,130,246,0.3)]'
            : 'border-slate-700 bg-slate-800'
        } ${disabled ? 'cursor-not-allowed opacity-50' : 'cursor-pointer'}`}
      >
        <span
          className={`inline-block h-5 w-5 rounded-full bg-white transition-transform ${
            checked ? 'translate-x-6' : 'translate-x-1'
          }`}
        />
      </button>
    </div>
  );
}
