import { useEffect, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, Card, Field, Input, selectClassName } from '@/components/ui';
import { getMessageTriggerModel, saveMessageTriggerModel, type MessageTriggerModelSettings as ModelSettings } from '@/api/messageTriggers';

interface ModelDraft {
  apiType: ModelSettings['api_type'];
  baseURL: string;
  model: string;
  apiKey: string;
  keyConfigured: boolean;
  clearAPIKey: boolean;
  timeout: string;
}

function toDraft(settings: ModelSettings): ModelDraft {
  return {
    apiType: settings?.api_type === 'responses' ? 'responses' : 'chat_completions',
    baseURL: settings?.base_url ?? '',
    model: settings?.model ?? '',
    apiKey: '',
    keyConfigured: settings?.key_configured === true,
    clearAPIKey: false,
    timeout: settings?.timeout_seconds?.toString() ?? '',
  };
}

type Feedback = { status: 'passed' } | { status: 'failed'; details: string };

function normalizedBaseURL(url: string) {
  return url.trim().replace(/\/+$/, '');
}

export default function MessageTriggerModelSettings({ onSaved }: { onSaved: () => void }) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState<ModelDraft | null>(null);
  const [loadCount, setLoadCount] = useState(0);
  const [loadError, setLoadError] = useState('');
  const [saving, setSaving] = useState(false);
  const [feedback, setFeedback] = useState<Feedback | null>(null);

  useEffect(() => {
    let active = true;
    getMessageTriggerModel().then(settings => {
      if (active) setDraft(toDraft(settings));
    }).catch((err: unknown) => {
      if (active) setLoadError(err instanceof Error ? err.message : String(err));
    });
    return () => { active = false; };
  }, [loadCount]);

  function update<K extends keyof ModelDraft>(field: K, value: ModelDraft[K]) {
    setDraft(current => current ? { ...current, [field]: value } : null);
    setFeedback(null);
  }

  function updateBaseURL(baseURL: string) {
    setDraft(current => {
      if (!current) return null;
      if (normalizedBaseURL(baseURL) === normalizedBaseURL(current.baseURL)) return { ...current, baseURL };
      return { ...current, baseURL, apiKey: '', clearAPIKey: false };
    });
    setFeedback(null);
  }

  const timeoutSeconds = draft?.timeout ? Number(draft.timeout) : undefined;
  const timeoutValid = timeoutSeconds === undefined || (Number.isInteger(timeoutSeconds) && timeoutSeconds >= 0 && timeoutSeconds <= 300);
  const valid = draft?.baseURL.trim() && draft.model.trim() && timeoutValid;

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!draft || !valid || saving) return;
    setSaving(true);
    setFeedback(null);
    try {
      const saved = await saveMessageTriggerModel({
        api_type: draft.apiType,
        base_url: draft.baseURL.trim(),
        model: draft.model.trim(),
        api_key: draft.clearAPIKey ? '' : draft.apiKey.trim() || undefined,
        timeout_seconds: timeoutSeconds,
      });
      setDraft(toDraft(saved));
      setFeedback({ status: 'passed' });
      onSaved();
    } catch (err) {
      setFeedback({ status: 'failed', details: err instanceof Error ? err.message : String(err) });
    } finally {
      setSaving(false);
    }
  }

  return (
    <Card className="space-y-4">
      <div>
        <h3 className="text-sm font-semibold text-gray-900 dark:text-white">{t('messageTriggerModel.title')}</h3>
        <p className="mt-1 text-sm text-gray-500 dark:text-gray-400">{t('messageTriggerModel.description')}</p>
      </div>
      {draft ? (
        <form className="max-w-2xl space-y-4" onSubmit={submit} aria-busy={saving}>
          <fieldset disabled={saving} className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={t('messageTriggerModel.protocol')}>
                <select className={selectClassName} value={draft.apiType} onChange={event => update('apiType', event.target.value === 'responses' ? 'responses' : 'chat_completions')}>
                  <option value="chat_completions">/chat/completions</option>
                  <option value="responses">/responses</option>
                </select>
              </Field>
              <Field label={t('messageTriggerModel.timeout')} hint={t('messageTriggerModel.timeoutHint')}>
                <Input type="number" min={0} max={300} step={1} placeholder="10" value={draft.timeout} onChange={event => update('timeout', event.target.value)} />
              </Field>
            </div>
            <Field label={t('messageTriggerModel.baseURL')} hint={t('messageTriggerModel.baseURLHint')}>
              <Input required inputMode="url" spellCheck={false} placeholder="https://api.example.com/v1" value={draft.baseURL} onChange={event => updateBaseURL(event.target.value)} />
            </Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={t('messageTriggerModel.model')}>
                <Input required spellCheck={false} value={draft.model} onChange={event => update('model', event.target.value)} />
              </Field>
              <div className="space-y-2">
                <Field label={t('messageTriggerModel.apiKey')} hint={t('messageTriggerModel.apiKeyHint')}>
                  <Input type="password" spellCheck={false} autoComplete="new-password" value={draft.apiKey} disabled={draft.clearAPIKey}
                    onChange={event => update('apiKey', event.target.value)} />
                </Field>
                <p className="text-xs text-gray-500 dark:text-gray-400">
                  {t(draft.keyConfigured ? 'messageTriggerModel.keyConfigured' : 'messageTriggerModel.keyMissing')}
                </p>
                {draft.keyConfigured && (
                  <label className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                    <input type="checkbox" checked={draft.clearAPIKey} onChange={event => update('clearAPIKey', event.target.checked)} className="accent-accent" />
                    {t('messageTriggerModel.clearAPIKey')}
                  </label>
                )}
              </div>
            </div>
          </fieldset>
          <Button type="submit" loading={saving} disabled={!valid}>{t(saving ? 'messageTriggerModel.testing' : 'messageTriggerModel.testAndSave')}</Button>
          {feedback?.status === 'passed' && <p role="status" className="text-sm text-emerald-700 dark:text-emerald-400">{t('messageTriggerModel.passed')}</p>}
          {feedback?.status === 'failed' && (
            <p role="alert" className="break-words text-sm text-red-600 dark:text-red-400">{t('messageTriggerModel.failed')} {feedback.details}</p>
          )}
        </form>
      ) : loadError ? (
        <div className="space-y-3">
          <p role="alert" className="break-words text-sm text-red-600 dark:text-red-400">{t('messageTriggerModel.loadFailed')} {loadError}</p>
          <Button type="button" variant="secondary" onClick={() => { setLoadError(''); setLoadCount(count => count + 1); }}>{t('common.refresh')}</Button>
        </div>
      ) : <p role="status" className="text-sm text-gray-500 dark:text-gray-400">{t('common.loading')}</p>}
    </Card>
  );
}
