import { cn } from '@/lib/utils';
import { cloneElement, useId, type InputHTMLAttributes, type ReactElement, type TextareaHTMLAttributes } from 'react';

export const selectClassName = 'w-full px-3 py-2 text-sm rounded-lg border border-gray-300/90 dark:border-white/[0.1] bg-white dark:bg-gray-950 text-gray-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-accent/45 focus:border-accent';

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactElement<{ id?: string; 'aria-describedby'?: string }> }) {
  const id = useId();
  const hintId = hint ? `${id}-hint` : undefined;
  return (
    <div className="space-y-1.5">
      <label htmlFor={id} className="block text-sm font-medium text-gray-700 dark:text-gray-300">{label}</label>
      {cloneElement(children, { id, 'aria-describedby': hintId })}
      {hint && <p id={hintId} className="text-xs text-gray-500 dark:text-gray-400">{hint}</p>}
    </div>
  );
}

interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  label?: string;
}

export function Input({ label, className, ...props }: InputProps) {
  return (
    <div className="space-y-1.5">
      {label && (
        <label className="block text-sm font-medium text-gray-700 dark:text-gray-300">{label}</label>
      )}
      <input
        className={cn(
          'w-full px-3 py-2 text-sm rounded-lg transition-all duration-200',
          'border border-gray-300/90 dark:border-white/[0.1]',
          'bg-white/90 backdrop-blur-sm dark:bg-[rgba(0,0,0,0.45)] dark:backdrop-blur-md',
          'text-gray-900 dark:text-white',
          'focus:outline-none focus:ring-2 focus:ring-accent/45 focus:border-accent',
          'placeholder:text-gray-400 dark:placeholder:text-gray-500',
          className
        )}
        {...props}
      />
    </div>
  );
}

interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  label?: string;
}

export function Textarea({ label, className, ...props }: TextareaProps) {
  return (
    <div className="space-y-1.5">
      {label && (
        <label className="block text-sm font-medium text-gray-700 dark:text-gray-300">{label}</label>
      )}
      <textarea
        className={cn(
          'w-full px-3 py-2 text-sm rounded-lg transition-all duration-200 resize-none',
          'border border-gray-300/90 dark:border-white/[0.1]',
          'bg-white/90 backdrop-blur-sm dark:bg-[rgba(0,0,0,0.45)] dark:backdrop-blur-md',
          'text-gray-900 dark:text-white',
          'focus:outline-none focus:ring-2 focus:ring-accent/45 focus:border-accent',
          'placeholder:text-gray-400 dark:placeholder:text-gray-500',
          className
        )}
        {...props}
      />
    </div>
  );
}
