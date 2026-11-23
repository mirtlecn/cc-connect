package dingtalk

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
)

type messageTriggerScope struct {
	mu           sync.RWMutex
	groups       []string
	allGroups    bool
	privateChats bool
}

var _ core.MessageTriggerPlatform = (*Platform)(nil)

func (p *Platform) SetMessageTriggerGroups(groups []string) {
	scope := &p.messageTriggerScope
	scope.mu.Lock()
	defer scope.mu.Unlock()
	scope.groups = slices.Clone(groups)
	scope.allGroups = false
}

func (p *Platform) SetMessageTriggerScope(groups []string, allGroups, privateChats bool) {
	scope := &p.messageTriggerScope
	scope.mu.Lock()
	defer scope.mu.Unlock()
	scope.groups = slices.Clone(groups)
	scope.allGroups = allGroups
	scope.privateChats = privateChats
}

func (p *Platform) triggerEvent(data *chatbot.BotCallbackDataModel) *core.MessageTriggerEvent {
	body := strings.TrimSpace(data.Text.Content)
	if data.Msgtype != "text" || body == "" || data.ConversationId == "" || data.MsgId == "" ||
		(data.SenderStaffId == "" && data.SenderId == "") ||
		(data.ChatbotUserId != "" && data.SenderId == data.ChatbotUserId) {
		return nil
	}

	privateChat := data.ConversationType == "1"
	groupChat := data.ConversationType == "2"
	scope := &p.messageTriggerScope
	scope.mu.RLock()
	selected := (privateChat && scope.privateChats) ||
		(groupChat && (scope.allGroups || slices.Contains(scope.groups, data.ConversationId)))
	scope.mu.RUnlock()
	if !selected {
		return nil
	}

	var mentionedIDs []string
	for _, user := range data.AtUsers {
		mentionedIDs = appendTriggerIDs(mentionedIDs, user.StaffId, user.DingtalkId)
	}
	// Stream bot callbacks are not a subscription to every group message. Only
	// native callback metadata can establish that the group mentioned this bot.
	directMention := groupChat && (data.IsInAtList ||
		(data.ChatbotUserId != "" && slices.Contains(mentionedIDs, data.ChatbotUserId)))
	if groupChat && !directMention {
		return nil
	}
	if directMention {
		mentionedIDs = appendTriggerIDs(mentionedIDs, data.ChatbotUserId)
	}

	return &core.MessageTriggerEvent{
		ChatID:           data.ConversationId,
		PrivateChat:      privateChat,
		Body:             body,
		SenderIDs:        appendTriggerIDs(nil, data.SenderStaffId, data.SenderId),
		MentionedIDs:     mentionedIDs,
		DirectBotMention: directMention,
		BotMentionKnown:  true,
	}
}

func appendTriggerIDs(ids []string, values ...string) []string {
	for _, id := range values {
		id = strings.TrimSpace(id)
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

func (p *Platform) TriggerReplyContext(msg *core.Message) (any, error) {
	rc, ok := msg.ReplyCtx.(replyContext)
	if !ok || msg.Trigger == nil || rc.conversationId == "" || rc.conversationId != msg.Trigger.ChatID ||
		rc.messageID == "" || rc.messageID != msg.MessageID || rc.isGroup == msg.Trigger.PrivateChat ||
		(rc.useProactiveAPI() && !rc.isGroup && rc.senderStaffId == "") {
		return nil, fmt.Errorf("dingtalk: trigger reply requires an addressable source conversation and message")
	}
	return rc, nil
}
