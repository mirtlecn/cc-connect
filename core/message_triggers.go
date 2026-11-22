package core

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MessageTriggerPlatform opts a platform into group message rules and native source replies.
type MessageTriggerPlatform interface {
	SetMessageTriggerGroups(groups []string)
	TriggerReplyContext(msg *Message) (any, error)
}

type messageTriggerCCPlatform interface {
	WithMessageTriggerCC(replyCtx any, userIDs []string) (any, error)
}

type messageTriggerReply struct {
	source    any
	ccUserIDs []string
}

type messageTriggerResultKey struct{}

// MessageTriggerResult returns the platform reply context carrying native
// mentions for the completed Agent response. Progress updates have no result.
func MessageTriggerResult(ctx context.Context) any {
	return ctx.Value(messageTriggerResultKey{})
}

func (r *messageTriggerReply) route(fallback any) any {
	if r != nil {
		return r.source
	}
	return fallback
}

func (r *messageTriggerReply) hasCC() bool {
	return r != nil && len(r.ccUserIDs) != 0
}

func (r *messageTriggerReply) resultContext(ctx context.Context, p Platform) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, messageTriggerResultKey{}, r.withCC(p))
}

func (r *messageTriggerReply) withCC(p Platform) any {
	if len(r.ccUserIDs) == 0 {
		return r.source
	}
	cc, ok := p.(messageTriggerCCPlatform)
	if !ok {
		slog.Warn("message trigger: platform cannot CC users", "platform", p.Name())
		return r.source
	}
	result, err := cc.WithMessageTriggerCC(r.source, r.ccUserIDs)
	if err != nil {
		slog.Warn("message trigger: CC unavailable", "platform", p.Name(), "error", err)
		return r.source
	}
	return result
}

// MessageTriggerEvent records identities from the platform event, independently of session routing.
type MessageTriggerEvent struct {
	ChatID           string
	Body             string
	SenderIDs        []string
	MentionedIDs     []string
	DirectBotMention bool
	BotMentionKnown  bool
	RuleOnly         bool // the existing platform path would ignore this message
}

type messageTriggerConfig struct {
	rules []MessageTriggerRule
	model MessageTriggerModel
}

type messageTriggerRuntime struct {
	mu     sync.Mutex // serializes updates to the rules and platform group subscriptions
	config atomic.Pointer[messageTriggerConfig]
	dedup  MessageDedup
	shells chan struct{}
	models chan struct{}

	confirmationMu sync.Mutex
	confirmations  map[string]messageTriggerConfirmation
	recalled       map[messageTriggerSource]time.Time
}

type messageTriggerSource struct {
	platform Platform
	id       string
}

// SetMessageTriggers publishes a validated configuration.
func (e *Engine) SetMessageTriggers(rules []MessageTriggerRule, model MessageTriggerModel) {
	e.messageTriggers.mu.Lock()
	defer e.messageTriggers.mu.Unlock()
	e.applyMessageTriggers(rules, model)
}

// ReloadMessageTriggers serializes reading and publishing with Web updates.
func (e *Engine) ReloadMessageTriggers(load func() ([]MessageTriggerRule, MessageTriggerModel, error)) error {
	e.messageTriggers.mu.Lock()
	defer e.messageTriggers.mu.Unlock()
	rules, model, err := load()
	if err != nil {
		return err
	}
	e.applyMessageTriggers(rules, model)
	return nil
}

func (e *Engine) updateMessageTriggerModel(model MessageTriggerModel) {
	e.messageTriggers.mu.Lock()
	defer e.messageTriggers.mu.Unlock()
	var rules []MessageTriggerRule
	if state := e.messageTriggers.config.Load(); state != nil {
		rules = state.rules
	}
	e.applyMessageTriggers(rules, model)
}

func (e *Engine) applyMessageTriggers(rules []MessageTriggerRule, model MessageTriggerModel) {
	normalized := make([]MessageTriggerRule, 0, len(rules))
	for _, input := range rules {
		rule, err := normalizeMessageTriggerRule(input)
		if err != nil {
			slog.Warn("message trigger: invalid runtime rule", "project", e.name, "rule", input.Name, "error", err)
			continue
		}
		normalized = append(normalized, rule)
	}
	e.messageTriggers.config.Store(&messageTriggerConfig{rules: normalized, model: model})
	for _, platform := range e.platforms {
		if triggers, ok := platform.(MessageTriggerPlatform); ok {
			var groups []string
			for _, rule := range normalized {
				if rule.Enabled && rule.Platform == platform.Name() {
					groups = append(groups, rule.groupsInScope...)
				}
			}
			triggers.SetMessageTriggerGroups(cleanTriggerList(groups))
		}
	}
}

func (e *Engine) matchMessageTrigger(p Platform, msg *Message) (*MessageTriggerRule, bool) {
	event := msg.Trigger
	state := e.messageTriggers.config.Load()
	if event == nil || state == nil || !event.BotMentionKnown || event.DirectBotMention ||
		event.ChatID == "" || msg.MessageID == "" || msg.IsPermissionResponse {
		return nil, false
	}
	text := strings.TrimSpace(event.Body)
	if text == "" || strings.HasPrefix(text, "/") || strings.HasPrefix(text, "!") || e.matchBannedWord(text) != "" {
		return nil, false
	}

	claimed := false
	ctx, cancel := context.WithTimeout(e.ctx, state.model.timeout())
	defer cancel()
	var intentSession *triggerIntentSession
	for i := len(state.rules) - 1; i >= 0; i-- {
		rule := &state.rules[i]
		if !rule.watches(p.Name(), event.ChatID) || !rule.matchesGroups(event) {
			continue
		}
		// Deduplicate before a model request and keep the same claim while checking
		// lower-priority rules.
		if !claimed {
			if e.messageTriggers.dedup.IsDuplicate(p.Name() + "\x00" + event.ChatID + "\x00" + msg.MessageID) {
				return nil, true
			}
			claimed = true
			msg.triggerRateChecked = true
			if !e.checkRateLimit(msg) {
				msg.triggerRateLimited = true
				return nil, false
			}
		}
		reason := ""
		if rule.TriggerMode == "intent" {
			if intentSession == nil {
				session := e.triggerIntentContext(msg)
				intentSession = &session
			}
			judgment, err := state.model.classify(ctx, triggerIntentInput{Prompt: rule.IntentPrompt, Session: *intentSession})
			if err != nil {
				slog.Warn("message trigger: intent evaluation failed", "project", e.name, "rule", rule.Name,
					"platform", p.Name(), "msg_id", msg.MessageID, "error", err)
				continue
			}
			if judgment.Decision == "no" {
				continue
			}
			if judgment.Decision == "confirm" {
				reason = judgment.Reason
			}
		}
		if e.messageTriggers.config.Load() != state || e.triggerSourceRecalled(p, msg.MessageID) {
			return nil, false
		}
		msg.triggerReason = reason
		return rule, false
	}
	return nil, false
}

func (e *Engine) mayEvaluateMessageIntent(p Platform, msg *Message) bool {
	event := msg.Trigger
	state := e.messageTriggers.config.Load()
	if event == nil || state == nil || !event.BotMentionKnown || event.DirectBotMention {
		return false
	}
	for i := len(state.rules) - 1; i >= 0; i-- {
		rule := state.rules[i]
		if rule.watches(p.Name(), event.ChatID) && rule.matchesGroups(event) {
			return rule.TriggerMode == "intent"
		}
	}
	return false
}

func (e *Engine) prepareMessageTrigger(p Platform, msg *Message) (*MessageTriggerRule, bool) {
	if msg.Trigger == nil {
		return nil, false
	}
	rule, duplicate := e.matchMessageTrigger(p, msg)
	if rule == nil {
		return nil, duplicate || msg.Trigger.RuleOnly
	}
	platform, ok := p.(MessageTriggerPlatform)
	if !ok {
		slog.Warn("message trigger: platform cannot reply to source", "project", e.name, "platform", p.Name())
		return nil, true
	}
	replyCtx, err := platform.TriggerReplyContext(msg)
	if err != nil || replyCtx == nil {
		slog.Warn("message trigger: source reply unavailable", "project", e.name, "platform", p.Name(),
			"rule", rule.Name, "msg_id", msg.MessageID, "error", err)
		return nil, true
	}
	msg.triggerReplyCtx = &messageTriggerReply{source: replyCtx}
	if rule.CCEnabled {
		msg.triggerReplyCtx.ccUserIDs = rule.CCUserIDs
	}
	slog.Info("message trigger matched", "project", e.name, "rule", rule.Name, "action", rule.Action,
		"platform", p.Name(), "msg_id", msg.MessageID)
	return rule, false
}

func (e *Engine) replyToMessageTrigger(p Platform, reply *messageTriggerReply, content, workspaceDir string) {
	content = e.renderOutgoingContentForWorkspace(p, content, workspaceDir)
	chunks := SplitMessageCodeFenceAware(content, maxPlatformMessageLen)
	for i, chunk := range chunks {
		source := reply.source
		if i == len(chunks)-1 {
			source = reply.withCC(p)
		}
		if err := e.replyWithError(p, source, chunk); err != nil {
			return
		}
	}
}
