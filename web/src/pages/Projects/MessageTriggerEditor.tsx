import { useId, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Button, Field, Input, Modal, Textarea, selectClassName } from '@/components/ui';
import type { MessageTriggerMode, MessageTriggerRule } from '@/api/messageTriggers';
import MessageTriggerConditionGroups, { createGroupDrafts, serializeGroups } from './MessageTriggerConditionGroups';
import { idListClassName, parseLines } from './MessageTriggerFields';

export const triggerPlatforms = ['feishu', 'lark', 'telegram'] as const;
type TriggerPlatform = typeof triggerPlatforms[number];

export const triggerModeLabels: Record<MessageTriggerMode, string> = {
  direct: 'messageTriggers.directMode',
  manual: 'messageTriggers.manualMode',
  intent: 'messageTriggers.intentMode',
};

export function isTriggerPlatform(platform: string): platform is TriggerPlatform {
  return triggerPlatforms.some(value => value === platform);
}

function isValidCcUserID(platform: string, id: string): boolean {
  if (platform === 'telegram') {
    return /^[1-9]\d{0,18}$/.test(id) && BigInt(id) <= 9223372036854775807n;
  }
  return /^ou_[A-Za-z0-9_]+$/.test(id);
}

interface EditorProps {
  rule?: MessageTriggerRule;
  platforms: TriggerPlatform[];
  saving: boolean;
  conflict: boolean;
  error: string;
  onClose: () => void;
  onSave: (rule: MessageTriggerRule) => void;
}

export default function MessageTriggerEditor({ rule, platforms, saving, conflict, error, onClose, onSave }: EditorProps) {
  const { t } = useTranslation();
  const ccHintId = useId();
  const [draft, setDraft] = useState(() => ({
    name: rule?.name ?? '',
    enabled: rule?.enabled ?? true,
    platform: rule?.platform ?? platforms[0] ?? '',
    groupMatch: rule?.group_match ?? 'or',
    groups: createGroupDrafts(rule?.groups),
    action: rule?.action ?? 'agent',
    triggerMode: rule?.trigger_mode ?? 'direct',
    intentPrompt: rule?.intent_prompt ?? '',
    ccEnabled: rule?.cc_enabled ?? false,
    ccUserIDs: rule?.cc_user_ids?.join('\n') ?? '',
    prompt: rule?.prompt ?? '',
    command: rule?.command ?? '',
  }));

  function update<K extends keyof typeof draft>(key: K, value: (typeof draft)[K]) {
    setDraft(previous => ({ ...previous, [key]: value }));
  }

  const groups = serializeGroups(draft.groups);
  const ccIDs = parseLines(draft.ccUserIDs);
  const invalidCcIDs = ccIDs.some(id => !isValidCcUserID(draft.platform, id));
  const platformAvailable = platforms.some(platform => platform === draft.platform);
  const intentPrompt = draft.intentPrompt.trim();
  const valid = draft.name.trim() && platformAvailable && groups !== null
    && (draft.triggerMode !== 'intent' || intentPrompt !== '')
    && (draft.action === 'agent' || draft.command.trim()) && !invalidCcIDs && (!draft.ccEnabled || ccIDs.length > 0);

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (saving || conflict || !valid || !groups) return;
    onSave({
      name: draft.name.trim(),
      enabled: draft.enabled,
      platform: draft.platform,
      group_match: draft.groupMatch,
      groups,
      action: draft.action,
      trigger_mode: draft.triggerMode,
      intent_prompt: draft.triggerMode === 'intent' ? intentPrompt : undefined,
      cc_enabled: draft.ccEnabled,
      cc_user_ids: ccIDs,
      prompt: draft.action === 'agent' ? draft.prompt : '',
      command: draft.action === 'shell' ? draft.command : '',
    });
  }

  return (
    <Modal open title={t(rule ? 'messageTriggers.edit' : 'messageTriggers.add')} onClose={onClose} className="max-h-[90dvh] max-w-2xl overflow-y-auto">
      <form onSubmit={submit}>
        <fieldset disabled={saving} className="space-y-5">
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t('messageTriggers.name')}>
              <Input required autoFocus value={draft.name} onChange={event => update('name', event.target.value)} />
            </Field>
            <Field label={t('messageTriggers.platform')}>
              <select className={selectClassName} required value={draft.platform} onChange={event => update('platform', event.target.value)}>
                {!platformAvailable && <option value={draft.platform} disabled>{t('messageTriggers.selectPlatform')}</option>}
                {platforms.map(platform => <option key={platform} value={platform}>{t(`messageTriggers.platforms.${platform}`)}</option>)}
              </select>
            </Field>
          </div>
          <MessageTriggerConditionGroups groups={draft.groups} groupMatch={draft.groupMatch} platform={draft.platform}
            onGroupsChange={groups => update('groups', groups)}
            onGroupMatchChange={match => update('groupMatch', match)} />
          <div className="space-y-3 border-t border-gray-200 pt-4 dark:border-white/10">
            <Field label={t('messageTriggers.triggerMode')} hint={draft.triggerMode === 'intent' ? undefined : t(`messageTriggers.${draft.triggerMode}ModeHint`)}>
              <select className={selectClassName} value={draft.triggerMode} onChange={event => update('triggerMode', event.target.value as MessageTriggerMode)}>
                {(Object.keys(triggerModeLabels) as MessageTriggerMode[]).map(mode => (
                  <option key={mode} value={mode}>{t(triggerModeLabels[mode])}</option>
                ))}
              </select>
            </Field>
            {draft.triggerMode === 'intent' && (
              <>
                <Field label={t('messageTriggers.intent')} hint={t('messageTriggers.intentHint')}>
                  <Textarea required rows={3} value={draft.intentPrompt} onChange={event => update('intentPrompt', event.target.value)} />
                </Field>
                <Link to="/system#message-trigger-model" target="_blank" rel="noreferrer" className="inline-block text-sm text-accent hover:underline">
                  {t('messageTriggers.modelSettingsLink')}
                </Link>
              </>
            )}
          </div>
          <div className="space-y-3 border-t border-gray-200 pt-4 dark:border-white/10">
            <Field label={t('messageTriggers.action')}>
              <select className={selectClassName} value={draft.action} onChange={event => update('action', event.target.value === 'shell' ? 'shell' : 'agent')}>
                <option value="agent">{t('messageTriggers.agent')}</option>
                <option value="shell">{t('messageTriggers.shell')}</option>
              </select>
            </Field>
            {draft.action === 'agent' ? (
              <Field label={t('messageTriggers.prompt')} hint={t('messageTriggers.promptHint')}>
                <Textarea rows={3} value={draft.prompt} onChange={event => update('prompt', event.target.value)} />
              </Field>
            ) : (
              <Field label={t('messageTriggers.command')} hint={t('messageTriggers.shellHint')}>
                <Textarea required rows={3} className="font-mono" placeholder="pwd" value={draft.command} onChange={event => update('command', event.target.value)} />
              </Field>
            )}
            <div className="space-y-1">
              <label className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                <input type="checkbox" checked={draft.ccEnabled} onChange={event => update('ccEnabled', event.target.checked)}
                  aria-describedby={ccHintId} className="accent-accent" />
                {t('messageTriggers.ccEnabled')}
              </label>
              <p id={ccHintId} className="pl-5 text-xs text-gray-500 dark:text-gray-400">
                {t('messageTriggers.ccHint')}
              </p>
            </div>
            <Field label={t('messageTriggers.ccUserIDs')} hint={t(draft.platform === 'telegram' ? 'messageTriggers.ccTelegramIDs' : 'messageTriggers.ccFeishuIDs')}>
              <Textarea required={draft.ccEnabled} rows={2} wrap="off" className={idListClassName} value={draft.ccUserIDs}
                aria-invalid={invalidCcIDs || undefined} onChange={event => update('ccUserIDs', event.target.value)} />
            </Field>
            {invalidCcIDs && <p role="alert" className="text-xs text-red-600 dark:text-red-400">{t('messageTriggers.ccInvalidIDs')}</p>}
          </div>
          <label className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input type="checkbox" checked={draft.enabled} onChange={event => update('enabled', event.target.checked)} className="accent-accent" />
            {t('messageTriggers.enabled')}
          </label>
        </fieldset>
        {error && <p role="alert" className="mt-4 text-sm text-red-600 dark:text-red-400">{error}</p>}
        <div className="mt-5 flex justify-end gap-2">
          <Button type="button" variant="secondary" disabled={saving} onClick={onClose}>{t('common.cancel')}</Button>
          <Button type="submit" loading={saving} disabled={conflict || !valid}>{t('common.save')}</Button>
        </div>
      </form>
    </Modal>
  );
}
