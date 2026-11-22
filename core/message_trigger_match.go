package core

import (
	"slices"
	"strings"
	"time"
)

func (r MessageTriggerRule) watches(platform string, event *MessageTriggerEvent) bool {
	if !r.Enabled || r.Platform != platform {
		return false
	}
	if event.PrivateChat {
		return r.privateChatsInScope
	}
	return r.allGroupsInScope || slices.Contains(r.groupsInScope, event.ChatID)
}

func (r MessageTriggerRule) matchesGroups(event *MessageTriggerEvent, at time.Time) bool {
	return matchTriggerItems(r.GroupMatch, r.Groups, func(group MessageTriggerGroup) bool {
		return group.matches(event, at)
	})
}

func (g MessageTriggerGroup) matches(event *MessageTriggerEvent, at time.Time) bool {
	if g.chatScope != nil {
		if !g.chatScope.matches(event, at) {
			return false
		}
		if len(g.Conditions) == 1 {
			return true
		}
	}
	return matchTriggerItems(g.Match, g.Conditions, func(condition MessageTriggerCondition) bool {
		if condition.Type == "chat_scope" {
			return g.Match == "and"
		}
		return condition.matches(event, at)
	})
}

// captureGroups keeps each regular expression's numbering independent. A nil
// result means there is no capturing regex, so existing shell syntax is kept.
func (r MessageTriggerRule) captureGroups(event *MessageTriggerEvent, at time.Time) []string {
	var captures []string
	for _, group := range r.Groups {
		matched := group.matches(event, at)
		for _, condition := range group.Conditions {
			if condition.compiled == nil || condition.compiled.NumSubexp() == 0 {
				continue
			}
			captures = []string{}
			if matched {
				if submatches := condition.compiled.FindStringSubmatch(event.Body); submatches != nil {
					return submatches[1:]
				}
			}
		}
	}
	return captures
}

func (c MessageTriggerCondition) matches(event *MessageTriggerEvent, at time.Time) bool {
	switch c.Type {
	case "chat_scope":
		if event.PrivateChat {
			return c.Value != "group"
		}
		return c.Value != "private" && (len(c.Values) == 0 || slices.Contains(c.Values, event.ChatID))
	case "group":
		return !event.PrivateChat && slices.Contains(c.Values, event.ChatID)
	case "sender":
		return matchesTriggerIDs(c.Values, event.SenderIDs)
	case "mentioned":
		return matchesTriggerIDs(c.Values, event.MentionedIDs)
	case "regex":
		return c.compiled != nil && c.compiled.MatchString(event.Body)
	case "time_range":
		minute := at.Hour()*60 + at.Minute()
		if c.startMinute > c.endMinute {
			return minute >= c.startMinute || minute < c.endMinute
		}
		return minute >= c.startMinute && minute < c.endMinute
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
