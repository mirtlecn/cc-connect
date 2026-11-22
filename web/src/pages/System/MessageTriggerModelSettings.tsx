import { useEffect, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, Card, Field, Input, selectClassName } from '@/components/ui';
import { getMessageTriggerModel, saveMessageTriggerModel, type MessageTriggerModelSettings as ModelSettings } from '@/api/messageTriggers';

interface ModelDraft {
  apiType: ModelSettings['api_type'];
  baseURL: string;
  model: string;
  apiKey: string;
  timeout: string;
}

function toDraft(settings: ModelSettings): ModelDraft {
  return {
    apiType: settings?.api_type === 'responses' ? 'responses' : 'chat_completions',
    baseURL: settings?.base_url ?? '',
    model: settings?.model ?? '',
    apiKey: settings?.api_key ?? '',
    timeout: settings?.timeout_seconds?.toString() ?? '',
  };
}

type Feedback = { status: 'passed' } | { status: 'failed'; details: string };

const endpointPaths: Record<ModelDraft['apiType'], string> = {
  chat_completions: '/chat/completions',
  responses: '/responses',
};

function modelRequestURL(baseURL: string, apiType: ModelDraft['apiType']): string {
  const address = baseURL.trim();
  if (!/^https?:\/\/[^/\s@]+(?:\/|$)/i.test(address) || /[\\?#]/.test(address)) return '';
  try {
    const url = new URL(address);
    url.pathname = (url.pathname + endpointPaths[apiType]).replace(/\/{2,}/g, '/');
    return url.href;
  } catch {
    return '';
  }
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

  const timeoutSeconds = draft?.timeout ? Number(draft.timeout) : undefined;
  const timeoutValid = timeoutSeconds === undefined || (Number.isInteger(timeoutSeconds) && timeoutSeconds >= 0 && timeoutSeconds <= 300);
  const valid = draft?.baseURL.trim() && draft.model.trim() && timeoutValid;
  const requestURL = draft ? modelRequestURL(draft.baseURL, draft.apiType) : '';

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
        api_key: draft.apiKey,
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
                  <option value="chat_completions">{endpointPaths.chat_completions}</option>
                  <option value="responses">{endpointPaths.responses}</option>
                </select>
              </Field>
              <Field label={t('messageTriggerModel.timeout')} hint={t('messageTriggerModel.timeoutHint')}>
                <Input type="number" min={0} max={300} step={1} placeholder="10" value={draft.timeout} onChange={event => update('timeout', event.target.value)} />
              </Field>
            </div>
            <div className="space-y-2">
              <Field label={t('messageTriggerModel.baseURL')} hint={t('messageTriggerModel.baseURLHint')}>
                <Input required inputMode="url" spellCheck={false} placeholder="https://api.example.com/v1" value={draft.baseURL} onChange={event => update('baseURL', event.target.value)} />
              </Field>
              {requestURL && (
                <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
                  <span>{t('messageTriggerModel.requestURL')}</span>
                  <code className="min-w-0 break-all text-gray-700 dark:text-gray-200">{requestURL}</code>
                </div>
              )}
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={t('messageTriggerModel.model')}>
                <Input required spellCheck={false} value={draft.model} onChange={event => update('model', event.target.value)} />
              </Field>
              <Field label={t('messageTriggerModel.apiKey')}>
                <Input spellCheck={false} autoComplete="off" value={draft.apiKey} onChange={event => update('apiKey', event.target.value)} />
              </Field>
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
