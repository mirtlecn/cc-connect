package feishu

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	groupHistoryImagePlaceholder = "[image]"
	maxGroupHistoryImageMessages = 3
	maxGroupHistoryImages        = 6
	groupHistoryImageTimeout     = 6 * time.Second
)

type groupHistoryImage struct {
	offset int // byte offset of the generated placeholder in the history text
	key    string
}

func (history groupHistoryContext) hasImages() bool {
	for _, entry := range history.entries {
		if len(entry.images) > 0 {
			return true
		}
	}
	return false
}

func (p *Platform) loadGroupHistoryImages(ctx context.Context, history groupHistoryContext, quoted []core.ImageAttachment) (string, []core.ImageAttachment) {
	ctx, cancel := context.WithTimeout(ctx, groupHistoryImageTimeout)
	defer cancel()
	resolved := make(map[uint64]map[int]core.ImageAttachment)
	remainingMessages := maxGroupHistoryImageMessages
	remainingImages := maxGroupHistoryImages
	for i := len(history.entries) - 1; i >= 0 && remainingMessages > 0 && remainingImages > 0 && ctx.Err() == nil; i-- {
		entry := history.entries[i]
		if len(entry.images) == 0 || p.isMessageRecalled(entry.messageID) {
			continue
		}
		remainingMessages--
		count := min(remainingImages, len(entry.images))
		remainingImages -= count
		for j, ref := range entry.images[:count] {
			if ctx.Err() != nil || p.isMessageRecalled(entry.messageID) {
				break
			}
			data, err := p.downloadResourceChunked(ctx, entry.messageID, ref.key, "image")
			if err != nil || len(data) == 0 {
				slog.Debug(p.tag()+": group history image unavailable", "message_id", entry.messageID, "error", err)
				continue
			}
			if resolved[entry.id] == nil {
				resolved[entry.id] = make(map[int]core.ImageAttachment)
			}
			image := core.ImageAttachment{}
			if !slices.ContainsFunc(quoted, func(candidate core.ImageAttachment) bool { return bytes.Equal(candidate.Data, data) }) {
				image = core.ImageAttachment{MimeType: detectMimeType(data), Data: data}
			}
			// A quoted copy already satisfies this placeholder without adding another attachment.
			resolved[entry.id][j] = image
		}
	}
	return p.renderGroupHistory(history, resolved)
}
