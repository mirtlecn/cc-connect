package core

import (
	"context"
	"log/slog"
	"slices"
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
	dedup  *MessageDedup
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
	e.clearMessageTriggerConfirmations(MessageProcessingFailed, false)
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

func (e *Engine) canMatchMessageTrigger(msg *Message) bool {
	event := msg.Trigger
	if event == nil || !event.BotMentionKnown ||
		event.ChatID == "" || msg.MessageID == "" || msg.IsPermissionResponse {
		return false
	}
	text := strings.TrimSpace(event.Body)
	return text != "" && !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "!") && e.matchBannedWord(text) == ""
}

func (e *Engine) matchMessageTrigger(p Platform, msg *Message, at time.Time, evaluateIntent bool) (*MessageTriggerRule, bool) {
	state := e.messageTriggers.config.Load()
	if state == nil || !e.canMatchMessageTrigger(msg) {
		return nil, false
	}
	event := msg.Trigger

	claimed := false
	var ctx context.Context
	var input *triggerIntentInput
	for i := len(state.rules) - 1; i >= 0; i-- {
		rule := &state.rules[i]
		if !rule.watches(p.Name(), event.ChatID) || !rule.matchesGroups(event, at) {
			continue
		}
		// Deduplicate before a model request and keep the same claim while checking
		// lower-priority rules.
		if !claimed {
			if e.messageTriggers.dedup.IsDuplicate(p.Name() + "\x00" + event.ChatID + "\x00" + msg.MessageID) {
				return nil, true
			}
			claimed = true
			msg.processingFeedback = startMessageProcessingFeedback(e.ctx, p, msg.ReplyCtx)
			msg.triggerRateChecked = true
			if !e.checkRateLimit(msg) {
				msg.triggerRateLimited = true
				return nil, false
			}
		}
		reason := ""
		if rule.TriggerMode == "intent" {
			if !evaluateIntent {
				return nil, false
			}
			if input == nil {
				input = &triggerIntentInput{Session: e.triggerIntentContext(msg)}
				if state.model.SendImages && state.model.available() {
					extra, images := e.triggerIntentImages(msg, state.model.protocol())
					input.Session.Trigger.Context = truncateStr(extra, 4000)
					input.Images = images
					if e.messageTriggers.config.Load() != state || e.triggerSourceRecalled(p, msg.MessageID) {
						return nil, false
					}
				}
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(e.ctx, state.model.timeout())
				defer cancel()
			}
			input.Prompt = rule.IntentPrompt
			judgment, err := state.model.classify(ctx, *input)
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

func (e *Engine) triggerIntentImages(msg *Message, protocol string) (string, []ImageAttachment) {
	const maxImages = 6
	// Base64 adds a third to inline media size. Leave room for the JSON input
	// within the Gemini API's 20 MB inline request limit.
	remaining := 12 * 1024 * 1024
	var images []ImageAttachment
	add := func(attachments []ImageAttachment) {
		for _, image := range attachments {
			if len(images) == maxImages {
				break
			}
			if len(image.Data) == 0 || len(image.Data) > remaining {
				continue
			}
			switch image.MimeType {
			case "image/png", "image/jpeg", "image/webp":
			case "image/gif":
				if protocol == "gemini" {
					continue
				}
			default:
				continue
			}
			images = append(images, image)
			remaining -= len(image.Data)
		}
	}
	add(msg.Images)
	if msg.LoadExtraContent != nil && len(images) < maxImages && remaining > 0 {
		currentCount := len(images)
		extra, historyImages := msg.LoadExtraContent(e.ctx)
		add(historyImages)
		if len(images)-currentCount == len(historyImages) {
			// The rendered context numbers history attachments from one.
			return extra, slices.Concat(images[currentCount:], images[:currentCount])
		}
	}
	return msg.ExtraContent, images
}

func (e *Engine) mayEvaluateMessageIntent(p Platform, msg *Message, at time.Time) bool {
	state := e.messageTriggers.config.Load()
	if state == nil || !e.canMatchMessageTrigger(msg) {
		return false
	}
	event := msg.Trigger
	for i := len(state.rules) - 1; i >= 0; i-- {
		rule := state.rules[i]
		if rule.watches(p.Name(), event.ChatID) && rule.matchesGroups(event, at) {
			return rule.TriggerMode == "intent"
		}
	}
	return false
}

func (e *Engine) prepareMessageTrigger(p Platform, msg *Message, at time.Time, evaluateIntent bool) (*MessageTriggerRule, bool) {
	if msg.Trigger == nil {
		return nil, false
	}
	rule, duplicate := e.matchMessageTrigger(p, msg, at, evaluateIntent)
	if e.triggerSourceRecalled(p, msg.MessageID) {
		msg.finishProcessingFeedback(MessageProcessingCancelled)
		return nil, true
	}
	if rule == nil {
		drop := duplicate || msg.Trigger.RuleOnly
		if drop || msg.triggerRateLimited {
			msg.finishProcessingFeedback(MessageProcessingFailed)
		} else {
			msg.finishProcessingFeedback(MessageProcessingHandOff)
		}
		return nil, drop
	}
	platform, ok := p.(MessageTriggerPlatform)
	if !ok {
		slog.Warn("message trigger: platform cannot reply to source", "project", e.name, "platform", p.Name())
		msg.finishProcessingFeedback(MessageProcessingFailed)
		return nil, true
	}
	replyCtx, err := platform.TriggerReplyContext(msg)
	if err != nil || replyCtx == nil {
		slog.Warn("message trigger: source reply unavailable", "project", e.name, "platform", p.Name(),
			"rule", rule.Name, "msg_id", msg.MessageID, "error", err)
		msg.finishProcessingFeedback(MessageProcessingFailed)
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

func startMessageProcessingFeedback(ctx context.Context, p Platform, replyCtx any) func(MessageProcessingOutcome, bool) {
	if platform, ok := p.(MessageProcessingFeedback); ok {
		return platform.StartMessageProcessingFeedback(ctx, replyCtx)
	}
	return nil
}

func (msg *Message) finishProcessingFeedback(outcome MessageProcessingOutcome) {
	if finish := msg.processingFeedback; finish != nil {
		msg.processingFeedback = nil
		finish(outcome, false)
	}
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
