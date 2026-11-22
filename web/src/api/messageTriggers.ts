import api from './client';

export type MessageTriggerMatch = 'and' | 'or';
export type MessageTriggerMode = 'direct' | 'manual' | 'intent';

export type MessageTriggerCondition =
  | { type: 'group' | 'sender' | 'mentioned'; values: string[] }
  | { type: 'regex'; value: string };

export interface MessageTriggerGroup {
  match: MessageTriggerMatch;
  conditions: MessageTriggerCondition[];
}

export interface MessageTriggerRule {
  name: string;
  enabled: boolean;
  platform: string;
  group_match: MessageTriggerMatch;
  groups: MessageTriggerGroup[];
  action: 'agent' | 'shell';
  trigger_mode: MessageTriggerMode;
  intent_prompt?: string;
  cc_enabled?: boolean;
  cc_user_ids?: string[];
  prompt?: string;
  command?: string;
}

export interface MessageTriggerSettings {
  rules: MessageTriggerRule[] | null;
  semantic_configured: boolean;
  revision: string;
}

export interface MessageTriggerModelSettings {
  api_type: 'responses' | 'chat_completions';
  base_url: string;
  model: string;
  timeout_seconds?: number;
  key_configured: boolean;
}

const triggerPath = (project: string) => `/projects/${encodeURIComponent(project)}/message-triggers`;

export const getMessageTriggers = (project: string) =>
  api.get<MessageTriggerSettings>(triggerPath(project));

export const saveMessageTriggers = (project: string, rules: MessageTriggerRule[], revision: string) =>
  api.put<MessageTriggerSettings>(triggerPath(project), { rules, revision });

export const getMessageTriggerModel = () => api.get<MessageTriggerModelSettings>('/message-trigger-model');

export const saveMessageTriggerModel = (settings: Omit<MessageTriggerModelSettings, 'key_configured'> & { api_key?: string }) =>
  api.put<MessageTriggerModelSettings>('/message-trigger-model', settings);
