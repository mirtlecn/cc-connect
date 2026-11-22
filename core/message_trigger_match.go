package core

import (
	"slices"
	"strings"
)

func (r MessageTriggerRule) watches(platform, chatID string) bool {
	return r.Enabled && r.Platform == platform && slices.Contains(r.groupsInScope, chatID)
}

func (r MessageTriggerRule) matchesGroups(event *MessageTriggerEvent) bool {
	return matchTriggerItems(r.GroupMatch, r.Groups, func(group MessageTriggerGroup) bool {
		return matchTriggerItems(group.Match, group.Conditions, func(condition MessageTriggerCondition) bool {
			return condition.matches(event)
		})
	})
}

func (c MessageTriggerCondition) matches(event *MessageTriggerEvent) bool {
	switch c.Type {
	case "group":
		return slices.Contains(c.Values, event.ChatID)
	case "sender":
		return matchesTriggerIDs(c.Values, event.SenderIDs)
	case "mentioned":
		return matchesTriggerIDs(c.Values, event.MentionedIDs)
	case "regex":
		return c.compiled != nil && c.compiled.MatchString(event.Body)
	default:
		return false
	}
}

func matchTriggerItems[T any](operator string, items []T, match func(T) bool) bool {
	if len(items) == 0 {
		return false
	}
	all := operator == "and"
	for _, item := range items {
		if matched := match(item); matched != all {
			return matched
		}
	}
	return all
}

func matchesTriggerIDs(filter, actual []string) bool {
	for _, expected := range filter {
		for _, id := range actual {
			if strings.EqualFold(expected, id) {
				return true
			}
		}
	}
	return false
}
