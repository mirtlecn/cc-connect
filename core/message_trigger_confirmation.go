package core

import (
	"context"
	"crypto/rand"
	"log/slog"
	"strings"
	"time"
)

const messageTriggerConfirmationTTL = 5 * time.Minute
const maxPendingMessageTriggerConfirmations = 128

type MessageTriggerConfirmationPlatform interface {
	SetMessageTriggerConfirmationHandler(func(id, chatID, userID string, approved bool) (feedback string, resolved bool))
	SendMessageTriggerConfirmation(ctx context.Context, replyCtx any, id, prompt, yesLabel, noLabel string, adminIDs []string) error
}

type messageTriggerConfirmation struct {
	platform Platform
	message  Message
	rule     *MessageTriggerRule
	expires  time.Time
}

func (e *Engine) requestMessageTriggerConfirmation(p Platform, msg *Message, rule *MessageTriggerRule) {
	platform, supported := p.(MessageTriggerConfirmationPlatform)
	adminIDs := e.messageTriggerAdmins()
	if !supported || len(adminIDs) == 0 {
		slog.Warn("message trigger: approval unavailable", "project", e.name, "platform", p.Name(),
			"rule", rule.Name, "interactive_buttons", supported, "explicit_admin_required", true)
		return
	}
	id := rand.Text()
	now := time.Now()
	e.messageTriggers.confirmationMu.Lock()
	if e.messageTriggers.confirmations == nil {
		e.messageTriggers.confirmations = make(map[string]messageTriggerConfirmation)
	}
	for token, pending := range e.messageTriggers.confirmations {
		if !now.Before(pending.expires) || !e.isCurrentMessageTrigger(pending.rule) {
			delete(e.messageTriggers.confirmations, token)
		}
	}
	if len(e.messageTriggers.confirmations) >= maxPendingMessageTriggerConfirmations || !e.isCurrentMessageTrigger(rule) ||
		e.messageTriggers.recalled[messageTriggerSource{p, msg.MessageID}].After(now) {
		e.messageTriggers.confirmationMu.Unlock()
		slog.Warn("message trigger: could not schedule approval", "project", e.name, "rule", rule.Name)
		return
	}
	e.messageTriggers.confirmations[id] = messageTriggerConfirmation{
		platform: p, message: *msg, rule: rule, expires: now.Add(messageTriggerConfirmationTTL),
	}
	e.messageTriggers.confirmationMu.Unlock()

	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	prompt := e.i18n.Tf(MsgMessageTriggerConfirm, truncateStr(rule.Name, 100))
	if msg.triggerReason != "" {
		prompt += "\n\n" + e.i18n.Tf(MsgMessageTriggerReason, truncateStr(msg.triggerReason, 500))
	}
	if err := platform.SendMessageTriggerConfirmation(ctx, msg.triggerReplyCtx.source, id, prompt,
		e.i18n.T(MsgMessageTriggerYes), e.i18n.T(MsgMessageTriggerNo), adminIDs); err != nil {
		e.messageTriggers.confirmationMu.Lock()
		delete(e.messageTriggers.confirmations, id)
		e.messageTriggers.confirmationMu.Unlock()
		slog.Warn("message trigger: confirmation could not be sent", "project", e.name,
			"platform", p.Name(), "rule", rule.Name, "error", err)
	}
}

func (e *Engine) messageTriggerAdmins() []string {
	e.userRolesMu.RLock()
	adminFrom := e.adminFrom
	e.userRolesMu.RUnlock()
	if adminFrom == "*" {
		return nil
	}
	return cleanTriggerList(strings.Split(adminFrom, ","))
}

func (e *Engine) isCurrentMessageTrigger(rule *MessageTriggerRule) bool {
	if config := e.messageTriggers.config.Load(); config != nil {
		for i := range config.rules {
			if &config.rules[i] == rule {
				return true
			}
		}
	}
	return false
}

func (e *Engine) resolveMessageTriggerConfirmation(p Platform, id, chatID, userID string, approved bool) (string, bool) {
	if userID == "" || !matchesTriggerIDs(e.messageTriggerAdmins(), []string{userID}) {
		return e.i18n.T(MsgMessageTriggerAdminOnly), false
	}
	e.messageTriggers.confirmationMu.Lock()
	pending, exists := e.messageTriggers.confirmations[id]
	if !exists || pending.platform != p || (chatID != "" && pending.message.Trigger.ChatID != chatID) {
		e.messageTriggers.confirmationMu.Unlock()
		return e.i18n.T(MsgMessageTriggerUnavailable), false
	}
	delete(e.messageTriggers.confirmations, id)
	e.messageTriggers.confirmationMu.Unlock()
	if e.ctx.Err() != nil || !time.Now().Before(pending.expires) || !e.isCurrentMessageTrigger(pending.rule) {
		return e.i18n.T(MsgMessageTriggerUnavailable), true
	}

	slog.Info("message trigger: admin decision", "project", e.name, "platform", p.Name(), "rule", pending.rule.Name,
		"msg_id", pending.message.MessageID, "admin", userID, "approved", approved)
	if !approved {
		return e.i18n.T(MsgMessageTriggerRejected), true
	}
	// After explicit approval it is a new admission for ordering purposes.
	// Keep the original message ID, sender, session and reply context.
	pending.message.UserMessageTimeMs = 0
	pending.message.triggerConfirmed = true
	go e.handleAcceptedMessage(p, &pending.message, pending.rule)
	return e.i18n.T(MsgMessageTriggerApproved), true
}

func (e *Engine) cancelMessageTriggerConfirmation(p Platform, messageID string) {
	e.messageTriggers.confirmationMu.Lock()
	defer e.messageTriggers.confirmationMu.Unlock()
	if e.messageTriggers.recalled == nil {
		e.messageTriggers.recalled = make(map[messageTriggerSource]time.Time)
	}
	now := time.Now()
	for source, until := range e.messageTriggers.recalled {
		if !until.After(now) {
			delete(e.messageTriggers.recalled, source)
		}
	}
	e.messageTriggers.recalled[messageTriggerSource{p, messageID}] = now.Add(2 * messageTriggerConfirmationTTL)
	for token, pending := range e.messageTriggers.confirmations {
		if pending.platform == p && pending.message.MessageID == messageID {
			delete(e.messageTriggers.confirmations, token)
		}
	}
}

func (e *Engine) triggerSourceRecalled(p Platform, messageID string) bool {
	e.messageTriggers.confirmationMu.Lock()
	defer e.messageTriggers.confirmationMu.Unlock()
	return e.messageTriggers.recalled[messageTriggerSource{p, messageID}].After(time.Now())
}
