import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus, Trash2 } from 'lucide-react';
import { Button, Field, Input, Textarea, selectClassName } from '@/components/ui';
import type { MessageTriggerChatScope, MessageTriggerCondition, MessageTriggerGroup, MessageTriggerMatch } from '@/api/messageTriggers';
import { idListClassName, parseLines } from './MessageTriggerFields';

type ConditionType = MessageTriggerCondition['type'];

export const conditionLabels: Record<ConditionType, string> = {
  chat_scope: 'messageTriggers.chatScope',
  group: 'messageTriggers.groups',
  sender: 'messageTriggers.senders',
  mentioned: 'messageTriggers.mentions',
  regex: 'messageTriggers.regex',
  time_range: 'messageTriggers.timeRange',
};

export const chatScopeLabels: Record<MessageTriggerChatScope, string> = {
  all: 'messageTriggers.chatScopes.all',
  group: 'messageTriggers.chatScopes.group',
  private: 'messageTriggers.chatScopes.private',
};

const conditionTypes = Object.keys(conditionLabels) as ConditionType[];
const chatScopes = Object.keys(chatScopeLabels) as MessageTriggerChatScope[];
const dailyTimePattern = /^(?:[01][0-9]|2[0-3]):[0-5][0-9]$/;

interface ConditionDraft {
  id: number;
  type: ConditionType | '';
  text: string;
  scope: MessageTriggerChatScope;
  start: string;
  end: string;
}

interface GroupDraft {
  id: number;
  match: MessageTriggerMatch;
  conditions: ConditionDraft[];
}

export function createGroupDrafts(groups: MessageTriggerGroup[] | undefined): GroupDraft[] {
  if (!groups?.length) return [{ id: 1, match: 'and', conditions: [] }];
  return groups.map((group, index) => ({
    id: index + 1,
    match: group.match ?? 'and',
    conditions: (group.conditions ?? []).map((condition, index) => ({
      id: index + 1,
      type: condition.type,
      text: 'values' in condition ? (condition.values ?? []).join('\n') : condition.type === 'regex' ? condition.value ?? '' : '',
      scope: condition.type === 'chat_scope' ? condition.value : 'all',
      start: condition.type === 'time_range' ? condition.start ?? '' : '',
      end: condition.type === 'time_range' ? condition.end ?? '' : '',
    })),
  }));
}

function serializeCondition(draft: ConditionDraft): MessageTriggerCondition | undefined {
  switch (draft.type) {
    case 'chat_scope': {
      const values = draft.scope === 'private' ? [] : parseLines(draft.text);
      return values.length ? { type: draft.type, value: draft.scope, values } : { type: draft.type, value: draft.scope };
    }
    case 'group':
    case 'sender':
    case 'mentioned': {
      const values = parseLines(draft.text);
      return values.length ? { type: draft.type, values } : undefined;
    }
    case 'regex':
      return draft.text.trim() ? { type: 'regex', value: draft.text } : undefined;
    case 'time_range':
      if (draft.start !== draft.end && dailyTimePattern.test(draft.start) && dailyTimePattern.test(draft.end)) {
        return { type: 'time_range', start: draft.start, end: draft.end };
      }
  }
}

export function serializeGroups(groups: GroupDraft[]): MessageTriggerGroup[] | null {
  if (!groups.length) return null;
  const result: MessageTriggerGroup[] = [];
  for (const group of groups) {
    if (!group.conditions.length || group.conditions.filter(condition => condition.type === 'chat_scope').length > 1) return null;
    const conditions: MessageTriggerCondition[] = [];
    for (const draft of group.conditions) {
      const condition = serializeCondition(draft);
      if (!condition) return null;
      conditions.push(condition);
    }
    result.push({ match: group.match, conditions });
  }
  return result;
}

function MatchSelect({ label, value, onChange }: { label: string; value: MessageTriggerMatch; onChange: (match: MessageTriggerMatch) => void }) {
  const { t } = useTranslation();
  return (
    <Field label={label}>
      <select className={selectClassName} value={value} onChange={event => onChange(event.target.value === 'or' ? 'or' : 'and')}>
        <option value="and">{t('messageTriggers.and')}</option>
        <option value="or">{t('messageTriggers.or')}</option>
      </select>
    </Field>
  );
}

interface ConditionEditorProps {
  condition: ConditionDraft;
  platform: string;
  scopeTaken: boolean;
  onChange: (next: ConditionDraft) => void;
  onRemove: () => void;
}

function ConditionEditor({ condition, platform, scopeTaken, onChange, onRemove }: ConditionEditorProps) {
  const { t } = useTranslation();
  const timeHintId = useId();
  const { type, text, scope, start, end } = condition;
  const visibleTypes = conditionTypes.filter(option => option !== 'group' || type === 'group');
  const sameTime = start !== '' && start === end;
  const dingTalkIDsHint = platform === 'dingtalk' ? t('messageTriggers.dingTalkUserIDsHint') : undefined;
  const hints = {
    group: t('messageTriggers.groupsHint'),
    sender: dingTalkIDsHint ?? t('messageTriggers.sendersHint'),
    mentioned: dingTalkIDsHint ?? t(platform === 'telegram' ? 'messageTriggers.mentionsTelegramHint' : 'messageTriggers.mentionsHint'),
    regex: t('messageTriggers.regexHint'),
  };

  return (
    <li className="space-y-3 border-t border-gray-200 pt-3 dark:border-white/10">
      <div className="flex items-end gap-2">
        <div className="min-w-0 flex-1">
          <Field label={t('messageTriggers.conditionType')}>
            <select className={selectClassName} value={type} autoFocus={type === ''} required
              onChange={event => onChange({ ...condition, type: event.target.value as ConditionType, text: '', scope: 'all', start: '', end: '' })}>
              <option value="" disabled>{t('messageTriggers.selectConditionType')}</option>
              {visibleTypes.map(option => (
                <option key={option} value={option} disabled={option === 'chat_scope' && scopeTaken && type !== 'chat_scope'}>
                  {t(conditionLabels[option])}
                </option>
              ))}
            </select>
          </Field>
        </div>
        <Button type="button" variant="ghost" title={t('messageTriggers.removeCondition')} aria-label={t('messageTriggers.removeCondition')} onClick={onRemove}>
          <Trash2 size={16} aria-hidden />
        </Button>
      </div>
      {type === 'chat_scope' ? (
        <div className="space-y-3">
          <Field label={t('messageTriggers.chatScope')}>
            <select className={selectClassName} value={scope}
              onChange={event => onChange({ ...condition, scope: event.target.value as MessageTriggerChatScope })}>
              {chatScopes.map(value => <option key={value} value={value}>{t(chatScopeLabels[value])}</option>)}
            </select>
          </Field>
          {scope !== 'private' && (
            <Field label={t('messageTriggers.groups')} hint={t('messageTriggers.chatScopeGroupsHint')}>
              <Textarea rows={2} wrap="off" className={idListClassName} value={text}
                onChange={event => onChange({ ...condition, text: event.target.value })} />
            </Field>
          )}
        </div>
      ) : type === 'time_range' ? (
        <fieldset aria-describedby={timeHintId} className="min-w-0 space-y-2">
          <legend className="sr-only">{t('messageTriggers.timeRange')}</legend>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={t('messageTriggers.timeStart')}>
              <Input type="time" step={60} required value={start} aria-invalid={sameTime || undefined}
                onChange={event => onChange({ ...condition, start: event.target.value })} />
            </Field>
            <Field label={t('messageTriggers.timeEnd')}>
              <Input type="time" step={60} required value={end} aria-invalid={sameTime || undefined}
                onChange={event => onChange({ ...condition, end: event.target.value })} />
            </Field>
          </div>
          <p id={timeHintId} className="text-xs text-gray-500 dark:text-gray-400">{t('messageTriggers.timeRangeHint')}</p>
          {sameTime && <p role="alert" className="text-xs text-red-600 dark:text-red-400">{t('messageTriggers.timeRangeSame')}</p>}
        </fieldset>
      ) : type && (
        <Field label={t(conditionLabels[type])} hint={hints[type]}>
          {type === 'regex' ? <Input className="font-mono" required value={text} onChange={event => onChange({ ...condition, text: event.target.value })} /> :
            <Textarea required rows={2} wrap="off" className={idListClassName}
              value={text} onChange={event => onChange({ ...condition, text: event.target.value })} />}
        </Field>
      )}
    </li>
  );
}

interface ConditionGroupsProps {
  groups: GroupDraft[];
  groupMatch: MessageTriggerMatch;
  platform: string;
  onGroupsChange: (groups: GroupDraft[]) => void;
  onGroupMatchChange: (match: MessageTriggerMatch) => void;
}

export default function MessageTriggerConditionGroups({ groups, groupMatch, platform, onGroupsChange, onGroupMatchChange }: ConditionGroupsProps) {
  const { t } = useTranslation();

  function updateGroup(group: GroupDraft) {
    onGroupsChange(groups.map(value => value.id === group.id ? group : value));
  }

  function addGroup() {
    const id = Math.max(0, ...groups.map(group => group.id)) + 1;
    onGroupsChange([...groups, { id, match: 'and', conditions: [] }]);
  }

  function addCondition(group: GroupDraft) {
    const id = Math.max(0, ...group.conditions.map(condition => condition.id)) + 1;
    updateGroup({ ...group, conditions: [...group.conditions, { id, type: '', text: '', scope: 'all', start: '', end: '' }] });
  }

  return (
    <div className="space-y-3 border-t border-gray-200 pt-4 dark:border-white/10">
      <h3 className="text-sm font-semibold text-gray-900 dark:text-white">{t('messageTriggers.conditions')}</h3>
      <p className="text-sm text-gray-600 dark:text-gray-400">{t('messageTriggers.conditionsHint')}</p>
      <MatchSelect label={t('messageTriggers.groupMatch')} value={groupMatch} onChange={onGroupMatchChange} />
      {groups.map((group, index) => (
        <fieldset key={group.id} className="min-w-0 space-y-3 rounded-lg border border-gray-200 px-4 pb-4 dark:border-white/10">
          <legend className="px-1 text-sm font-semibold text-gray-900 dark:text-white">{t('messageTriggers.groupNumber', { number: index + 1 })}</legend>
          <div className="flex items-end gap-2">
            <div className="min-w-0 flex-1">
              <MatchSelect label={t('messageTriggers.conditionMatch')} value={group.match}
                onChange={match => updateGroup({ ...group, match })} />
            </div>
            <Button type="button" variant="ghost" disabled={groups.length === 1} title={t('messageTriggers.removeGroup')}
              aria-label={t('messageTriggers.removeGroup')} onClick={() => onGroupsChange(groups.filter(value => value.id !== group.id))}>
              <Trash2 size={16} aria-hidden />
            </Button>
          </div>
          {group.conditions.length > 0 && (
            <ol className="space-y-3">
              {group.conditions.map(condition => (
                <ConditionEditor key={condition.id} condition={condition} platform={platform}
                  scopeTaken={group.conditions.some(value => value.type === 'chat_scope')}
                  onChange={next => updateGroup({ ...group, conditions: group.conditions.map(value => value.id === next.id ? next : value) })}
                  onRemove={() => updateGroup({ ...group, conditions: group.conditions.filter(value => value.id !== condition.id) })} />
              ))}
            </ol>
          )}
          <Button type="button" variant="secondary" size="sm" onClick={() => addCondition(group)}>
            <Plus size={14} aria-hidden />{t('messageTriggers.addCondition')}
          </Button>
        </fieldset>
      ))}
      <Button type="button" variant="secondary" size="sm" onClick={addGroup}>
        <Plus size={14} aria-hidden />{t('messageTriggers.addGroup')}
      </Button>
    </div>
  );
}
