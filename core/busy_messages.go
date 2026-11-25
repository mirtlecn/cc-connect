package core

import (
	"log/slog"
	"strings"
	"unicode"
)

// SetBusyMessageMode sets how an ordinary message is handled while a turn is
// running. By default, steer where supported and queue otherwise.
func (e *Engine) SetBusyMessageMode(mode string) {
	e.queueBusyMessages.Store(mode == "queue")
}

func (e *Engine) disabledCommandsForUser(userID string) map[string]bool {
	e.userRolesMu.RLock()
	disabled := e.disabledCmds
	roles := e.userRoles
	e.userRolesMu.RUnlock()
	if roles != nil {
		if role := roles.ResolveRole(userID); role != nil {
			return role.DisabledCmds
		}
	}
	return disabled
}

// SplitPSFollowUp recognizes /ps or /btw at an edge of a nonempty message.
func SplitPSFollowUp(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if body, ok := splitPSPrefix(text); ok {
		return body, true
	}
	if strings.HasPrefix(text, "/") {
		return "", false
	}
	if lastSpace := strings.LastIndexFunc(text, unicode.IsSpace); lastSpace >= 0 &&
		isPSMarker(strings.TrimSpace(text[lastSpace:])) {
		return strings.TrimSpace(text[:lastSpace]), true
	}
	return "", false
}

func splitPSPrefix(text string) (string, bool) {
	if firstSpace := strings.IndexFunc(text, unicode.IsSpace); firstSpace >= 0 && isPSMarker(text[:firstSpace]) {
		body := strings.TrimSpace(text[firstSpace:])
		return body, body != ""
	}
	return "", false
}

func isPSMarker(word string) bool {
	return strings.EqualFold(word, "/ps") || strings.EqualFold(word, "/btw")
}

func (e *Engine) maybeSteerBusyMessage(p Platform, msg *Message, key string, session *Session, sessions *SessionManager) bool {
	if msg.ModeOverride != "" || e.disabledCommandsForUser(msg.UserID)["ps"] {
		return false
	}
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if state == nil {
		return false
	}

	// Serialize automatic supplements so their acknowledgements and fallback
	// decisions follow the same order as delivery to the current turn.
	state.steeringMu.Lock()
	defer state.steeringMu.Unlock()
	state.mu.Lock()
	steerer, ok := state.agentSession.(TurnSteerer)
	if !ok || state.stopped || state.turnCancelled || state.busySession != session || !session.Busy() ||
		state.pending != nil || state.pendingUserInput != nil || len(state.pendingMessages) != 0 {
		state.mu.Unlock()
		return false
	}
	if e.isStaleUserMessageLocked(state, msg.UserMessageTimeMs) {
		snap := userMessageWatermarkSnapshotLocked(state)
		state.mu.Unlock()
		e.logStaleUserMessageDropped("reject_before_steer", msg, key, snap)
		return true
	}
	turnID := steerer.ActiveTurnID()
	state.mu.Unlock()
	if turnID == "" {
		return false
	}

	if msg.loadAgentContext != nil {
		e.loadMessageAgentContext(msg.loadAgentContext, &msg.Content, &msg.Images)
		msg.loadAgentContext = nil
	}
	if e.ctx.Err() != nil || e.triggerSourceRecalled(p, msg.MessageID) {
		return true
	}
	prompt := e.buildSenderPrompt(msg.Content, msg.UserID, msg.UserName, msg.Platform, msg.SessionKey, msg.ChannelKey)
	accepted, err := steerer.SteerActiveTurn(turnID, prompt, msg.MessageID, msg.Images, msg.Files)
	if err != nil {
		// A transport error may have occurred after delivery. Avoid sending the
		// same input again as a separate turn without a definite rejection.
		slog.Warn("failed to steer busy message", "session", key, "error", err)
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPsSendFailed))
		return true
	}
	if !accepted {
		return false
	}
	state.mu.Lock()
	state.lastSteeredUserMessageTimeMs = max(state.lastSteeredUserMessageTimeMs, msg.UserMessageTimeMs)
	state.mu.Unlock()
	session.TouchUserActivity()
	session.AddHistory("user", msg.Content)
	sessions.Save()
	runMessageAccepted(msg)
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPsSent))
	return true
}
