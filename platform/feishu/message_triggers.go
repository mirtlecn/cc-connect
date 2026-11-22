package feishu

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strings"

	"github.com/chenhg5/cc-connect/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const triggerConfirmationPrefix = "trigger-confirm:"
const triggerConfirmationRouteKey = "trigger_route"

var triggerMentionPlaceholder = regexp.MustCompile(`@_user_[0-9]+`)

func (p *Platform) SetMessageTriggerGroups(groups []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messageTriggerGroups = slices.Clone(groups)
}

func (p *Platform) watchesTriggerGroup(chatID string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return slices.Contains(p.messageTriggerGroups, chatID)
}

func feishuTriggerIDs(id *larkim.UserId) []string {
	var ids []string
	if id == nil {
		return ids
	}
	for _, value := range []*string{id.OpenId, id.UserId, id.UnionId} {
		if s := stringValue(value); s != "" && !slices.Contains(ids, s) {
			ids = append(ids, s)
		}
	}
	return ids
}

func (p *Platform) triggerEvent(msg *larkim.EventMessage, senderID *larkim.UserId, senderType, botOpenID string) *core.MessageTriggerEvent {
	chatID := stringValue(msg.ChatId)
	if stringValue(msg.ChatType) != "group" || !isGroupHistoryMessageType(stringValue(msg.MessageType)) ||
		!p.watchesTriggerGroup(chatID) || senderType != "user" {
		return nil
	}
	senderIDs := feishuTriggerIDs(senderID)
	if len(senderIDs) == 0 {
		return nil
	}
	event := &core.MessageTriggerEvent{
		ChatID: chatID, SenderIDs: senderIDs, BotMentionKnown: botOpenID != "",
		DirectBotMention: botOpenID != "" && isBotMentioned(msg.Mentions, botOpenID),
	}
	mentionKeys := make(map[string]struct{}, len(msg.Mentions))
	for _, mention := range msg.Mentions {
		if mention == nil || mention.Id == nil || stringValue(mention.Id.OpenId) == "" {
			event.BotMentionKnown = false
			continue
		}
		mentionKeys[stringValue(mention.Key)] = struct{}{}
		event.MentionedIDs = append(event.MentionedIDs, feishuTriggerIDs(mention.Id)...)
	}
	// Rich text can supply a native OpenID directly in an at element, or
	// refer to the event's mention list by placeholder.
	if stringValue(msg.MessageType) == "text" {
		var body struct{ Text string }
		if json.Unmarshal([]byte(stringValue(msg.Content)), &body) != nil {
			event.BotMentionKnown = false
		}
		for _, placeholder := range triggerMentionPlaceholder.FindAllString(body.Text, -1) {
			if _, resolved := mentionKeys[placeholder]; !resolved {
				event.BotMentionKnown = false
			}
		}
	} else {
		if post := p.parsePostBody(stringValue(msg.Content)); post != nil {
			for _, line := range post.Content {
				for _, elem := range line {
					if elem.Tag != "at" || elem.UserId == "all" {
						continue
					}
					if _, resolved := mentionKeys[elem.UserId]; resolved {
						continue
					}
					if strings.HasPrefix(elem.UserId, "ou_") && isValidFeishuLookupID(elem.UserId) {
						event.MentionedIDs = append(event.MentionedIDs, elem.UserId)
						event.DirectBotMention = event.DirectBotMention || elem.UserId == botOpenID
					} else {
						event.BotMentionKnown = false
					}
				}
			}
		} else {
			event.BotMentionKnown = false
		}
	}
	return event
}

func (p *Platform) TriggerReplyContext(msg *core.Message) (any, error) {
	rc, ok := msg.ReplyCtx.(replyContext)
	if !ok || msg.Trigger == nil || rc.chatID == "" || rc.chatID != msg.Trigger.ChatID ||
		rc.messageID == "" || rc.messageID != msg.MessageID || !strings.HasPrefix(msg.UserID, "ou_") ||
		!isValidFeishuLookupID(msg.UserID) {
		return nil, fmt.Errorf("%s: trigger reply requires the source message and sender OpenID", p.tag())
	}
	name := msg.UserName
	if name == "" {
		name = msg.UserID
	}
	rc.triggerMention = fmt.Sprintf(`<at user_id="%s">%s</at>`, msg.UserID, html.EscapeString(name))
	return rc, nil
}

func (p *Platform) replyToTrigger(ctx context.Context, rc replyContext, content string) error {
	msgType, body := buildReplyContent(rc.triggerMention + "\n" + content + rc.triggerCC)
	return p.replyMessage(ctx, rc, msgType, body)
}

func (p *Platform) WithMessageTriggerCC(rctx any, userIDs []string) (any, error) {
	rc, ok := rctx.(replyContext)
	if !ok || rc.triggerMention == "" {
		return nil, fmt.Errorf("%s: cc requires the trigger reply context", p.tag())
	}
	mentions := feishuTriggerMentions(userIDs, `<at user_id="%s"></at>`)
	if mentions == "" {
		return nil, fmt.Errorf("%s: cc requires a Feishu user OpenID", p.tag())
	}
	rc.triggerCC = "\n\ncc " + mentions
	return rc, nil
}

func feishuTriggerMentions(userIDs []string, format string) string {
	seen := make(map[string]struct{}, len(userIDs))
	mentions := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		id = strings.TrimSpace(id)
		if !strings.HasPrefix(id, "ou_") || len(id) <= len("ou_") || !isValidFeishuLookupID(id) {
			continue
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			mentions = append(mentions, fmt.Sprintf(format, id))
		}
	}
	return strings.Join(mentions, " ")
}

func (p *Platform) SetMessageTriggerConfirmationHandler(handler func(id, chatID, userID string, approved bool) (feedback string, resolved bool)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.triggerConfirmationHandler = handler
}

func (p *Platform) SendMessageTriggerConfirmation(ctx context.Context, rctx any, id, prompt, yesLabel, noLabel string, adminIDs []string) error {
	rc, ok := rctx.(replyContext)
	if !ok || rc.messageID == "" {
		return fmt.Errorf("%s: trigger confirmation requires the source message", p.tag())
	}
	mentions := feishuTriggerMentions(adminIDs, `<at id=%s></at>`)
	if mentions == "" {
		return fmt.Errorf("%s: trigger confirmation requires a Feishu administrator OpenID", p.tag())
	}
	// Several projects can share a single bot and a group. Include the
	// originating adapter's routing key so an unrelated project's expired
	// response cannot intercept the click before it reaches the owner.
	p.mu.Lock()
	if p.triggerConfirmationRoute == "" {
		p.triggerConfirmationRoute = rand.Text()
	}
	route := p.triggerConfirmationRoute
	p.mu.Unlock()
	yes := core.PrimaryBtn(yesLabel, triggerConfirmationPrefix+id+":y")
	no := core.DefaultBtn(noLabel, triggerConfirmationPrefix+id+":n")
	yes.Extra = map[string]string{triggerConfirmationRouteKey: route}
	no.Extra = yes.Extra
	card := core.NewCard().Markdown(html.EscapeString(prompt)+"\n\ncc "+mentions).Buttons(yes, no).Build()
	return p.replyMessage(ctx, rc, larkim.MsgTypeInteractive, renderCard(card, ""))
}

func (p *Platform) onTriggerConfirmation(data, route, chatID, userID string) *callback.CardActionTriggerResponse {
	id, choice, ok := strings.Cut(data, ":")
	if !ok || id == "" || (choice != "y" && choice != "n") {
		return nil
	}
	p.mu.RLock()
	handler := p.triggerConfirmationHandler
	owned := route != "" && route == p.triggerConfirmationRoute
	p.mu.RUnlock()
	if !owned || handler == nil {
		return nil
	}
	feedback, resolved := handler(id, chatID, userID, choice == "y")
	response := &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: "info", Content: feedback},
	}
	if resolved {
		response.Card = &callback.Card{
			Type: "raw",
			Data: renderCardMap(core.NewCard().Markdown(feedback).Build(), ""),
		}
	}
	return response
}
