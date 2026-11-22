import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ArrowDown, ArrowUp, Pencil, Plus, Trash2 } from 'lucide-react';
import { Badge, Button, Card, EmptyState } from '@/components/ui';
import { ApiError } from '@/api/client';
import { getMessageTriggers, saveMessageTriggers, type MessageTriggerRule, type MessageTriggerSettings } from '@/api/messageTriggers';
import type { ProjectDetail } from '@/api/projects';
import MessageTriggerEditor, { isTriggerPlatform, triggerModeLabels } from './MessageTriggerEditor';
import { conditionLabels } from './MessageTriggerConditionGroups';

export default function ProjectMessageTriggers({ project }: { project: ProjectDetail }) {
  const { t } = useTranslation();
  const [settings, setSettings] = useState<MessageTriggerSettings>();
  const [reloadCount, setReloadCount] = useState(0);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [editor, setEditor] = useState<number | 'new' | null>(null);

  const platforms = [...new Set((project.platform_configs ?? project.platforms).map(platform => platform.type))].filter(isTriggerPlatform);
  const rules = settings?.rules ?? [];
  const canWrite = !saving && !conflict;

  useEffect(() => {
    let active = true;
    getMessageTriggers(project.name).then(data => {
      if (active) setSettings(data);
    }).catch((err: unknown) => {
      if (active) setError(err instanceof Error ? err.message : String(err));
    });
    return () => { active = false; };
  }, [project.name, reloadCount]);

  async function save(nextRules: MessageTriggerRule[]) {
    if (!settings || !canWrite) return;
    setSaving(true);
    setError('');
    try {
      setSettings(await saveMessageTriggers(project.name, nextRules, settings.revision));
      setEditor(null);
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        setConflict(true);
      } else {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      setSaving(false);
    }
  }

  function edit(index: number | 'new' | null) {
    if (saving) return;
    setError('');
    setEditor(index);
  }

  function reload() {
    setSettings(undefined);
    setError('');
    setConflict(false);
    setReloadCount(count => count + 1);
  }

  function saveRule(rule: MessageTriggerRule) {
    if (editor === 'new') {
      void save([...rules, rule]);
    } else if (editor !== null) {
      void save(rules.map((value, index) => index === editor ? rule : value));
    }
  }

  function move(index: number, offset: -1 | 1) {
    const target = index + offset;
    const nextRules = [...rules];
    [nextRules[index], nextRules[target]] = [nextRules[target], nextRules[index]];
    void save(nextRules);
  }

  function remove(index: number) {
    if (window.confirm(t('messageTriggers.confirmDelete', { name: rules[index].name }))) {
      void save(rules.filter((_, i) => i !== index));
    }
  }

  if (!settings) {
    return error ? (
      <Card className="space-y-3">
        <p role="alert" className="text-sm text-red-600 dark:text-red-400">{error}</p>
        <Button variant="secondary" onClick={reload}>{t('messageTriggers.retry')}</Button>
      </Card>
    ) : <EmptyState message={t('common.loading')} />;
  }

  return (
    <Card className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold text-gray-900 dark:text-white">{t('projects.tabs.messageTriggers')}</h3>
          <p className="mt-1 text-sm text-gray-500 dark:text-gray-400">{t('messageTriggers.order')}</p>
        </div>
        {platforms.length > 0 && (
          <Button size="sm" onClick={() => edit('new')} disabled={!canWrite}>
            <Plus size={14} aria-hidden /> {t('messageTriggers.add')}
          </Button>
        )}
      </div>

      {editor === null && error && <p role="alert" className="text-sm text-red-600 dark:text-red-400">{error}</p>}
      {editor === null && conflict && (
        <div className="flex flex-wrap items-center gap-3">
          <p role="alert" className="text-sm text-red-600 dark:text-red-400">{t('messageTriggers.conflict')}</p>
          <Button size="sm" variant="secondary" onClick={reload}>{t('common.refresh')}</Button>
        </div>
      )}

      {rules.length === 0 ? (
        <EmptyState message={t(platforms.length ? 'messageTriggers.empty' : 'messageTriggers.noPlatforms')} />
      ) : (
        <ul className="divide-y divide-gray-200 dark:divide-white/10" aria-busy={saving}>
          {rules.map((rule, index) => {
            const toggle = t(rule.enabled ? 'messageTriggers.disable' : 'messageTriggers.enable');
            const expression = rule.groups?.map(group => {
              const labels = group.conditions.map(condition => {
                const label = t(conditionLabels[condition.type]);
                return condition.type === 'time_range' ? `${label} ${condition.start}–${condition.end}` : label;
              });
              return `(${labels.join(group.match === 'or' ? ' OR ' : ' AND ')})`;
            }).join(rule.group_match === 'and' ? ' AND ' : ' OR ');
            return (
              <li key={`${rule.name}-${index}`} className="flex flex-wrap items-center justify-between gap-3 py-3">
                <div className="min-w-0 space-y-1.5">
                  <h4 className="break-words text-sm font-medium text-gray-900 dark:text-white">{rule.name}</h4>
                  <div className="flex flex-wrap gap-1.5">
                    <Badge>{isTriggerPlatform(rule.platform) ? t(`messageTriggers.platforms.${rule.platform}`) : rule.platform}</Badge>
                    <Badge>{t(`messageTriggers.${rule.action}`)}</Badge>
                    <Badge variant="outline">{t(triggerModeLabels[rule.trigger_mode ?? 'direct'])}</Badge>
                    {rule.cc_enabled && <Badge variant="outline">{t('messageTriggers.ccCount', { total: rule.cc_user_ids?.length ?? 0 })}</Badge>}
                    <Badge variant={rule.enabled ? 'success' : 'default'}>{t(rule.enabled ? 'messageTriggers.enabled' : 'messageTriggers.disabled')}</Badge>
                  </div>
                  {expression && <p className="break-words text-xs text-gray-500 dark:text-gray-400">{expression}</p>}
                </div>
                <div className="flex flex-wrap gap-1">
                  <Button size="sm" variant="secondary" onClick={() => save(rules.map((value, i) => i === index ? { ...value, enabled: !value.enabled } : value))}
                    disabled={!canWrite} aria-label={`${toggle} ${rule.name}`}>{toggle}</Button>
                  <Button size="sm" variant="ghost" title={t('messageTriggers.edit')} aria-label={`${t('messageTriggers.edit')} ${rule.name}`} onClick={() => edit(index)} disabled={!canWrite}>
                    <Pencil size={14} aria-hidden />
                  </Button>
                  <Button size="sm" variant="ghost" title={t('messageTriggers.moveUp')} aria-label={`${t('messageTriggers.moveUp')} ${rule.name}`} onClick={() => move(index, -1)} disabled={!canWrite || index === 0}>
                    <ArrowUp size={14} aria-hidden />
                  </Button>
                  <Button size="sm" variant="ghost" title={t('messageTriggers.moveDown')} aria-label={`${t('messageTriggers.moveDown')} ${rule.name}`} onClick={() => move(index, 1)} disabled={!canWrite || index === rules.length - 1}>
                    <ArrowDown size={14} aria-hidden />
                  </Button>
                  <Button size="sm" variant="danger" title={t('common.delete')} aria-label={`${t('common.delete')} ${rule.name}`} onClick={() => remove(index)} disabled={!canWrite}>
                    <Trash2 size={14} aria-hidden />
                  </Button>
                </div>
              </li>
            );
          })}
        </ul>
      )}

      {editor !== null && (
        <MessageTriggerEditor
          rule={editor === 'new' ? undefined : rules[editor]}
          platforms={platforms}
          saving={saving}
          conflict={conflict}
          error={conflict ? t('messageTriggers.conflictEditor') : error}
          onClose={() => edit(null)}
          onSave={saveRule}
        />
      )}
    </Card>
  );
}
