package ui

import "sort"

// rankReactionUsage returns usage's keys sorted by count descending (ties
// broken by emoji so the order is stable) — this user's own most-sent
// reactions first, fed to emojipicker.New as its "recent" seed so the
// picker's default grid converges on what this user actually reaches for
// instead of always showing the same built-in common list.
func rankReactionUsage(usage map[string]int) []string {
	ranked := make([]string, 0, len(usage))
	for e := range usage {
		ranked = append(ranked, e)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if usage[ranked[i]] != usage[ranked[j]] {
			return usage[ranked[i]] > usage[ranked[j]]
		}
		return ranked[i] < ranked[j]
	})
	return ranked
}

// copyEmojiUsage returns an independent copy of usage (never nil, so Model
// can always increment into it directly), so Model doesn't share mutable
// state with the config.State map it was seeded from.
func copyEmojiUsage(usage map[string]int) map[string]int {
	out := make(map[string]int, len(usage))
	for e, n := range usage {
		out[e] = n
	}
	return out
}

// myReactionsEmoji returns the emoji we've already placed on a message, for
// seeding the picker's initial multi-select set (see emojipicker.SetPicked)
// so reopening it on something already reacted to starts with those cells
// toggled on instead of empty.
func myReactionsEmoji(reactions []Reaction) []string {
	mine := make([]string, 0, len(reactions))
	for _, r := range reactions {
		if r.Mine {
			mine = append(mine, r.Emoji)
		}
	}
	return mine
}

// setMyReactions recomputes a message's aggregate Reactions after our own
// contribution changes from oldMine to newMine, preserving every other
// reactor's counts — we only track aggregates here, not per-reactor
// breakdowns, so this reconciles the diff rather than rebuilding from
// scratch.
func setMyReactions(reactions []Reaction, newMine []string) []Reaction {
	newSet := make(map[string]bool, len(newMine))
	for _, e := range newMine {
		newSet[e] = true
	}

	result := make([]Reaction, 0, len(reactions)+len(newMine))
	seen := make(map[string]bool, len(reactions))
	for _, r := range reactions {
		seen[r.Emoji] = true
		isMine := newSet[r.Emoji]
		count := r.Count
		switch {
		case r.Mine && !isMine:
			count--
		case !r.Mine && isMine:
			count++
		}
		if count > 0 {
			result = append(result, Reaction{Emoji: r.Emoji, Count: count, Mine: isMine})
		}
	}
	for _, e := range newMine {
		if !seen[e] {
			result = append(result, Reaction{Emoji: e, Count: 1, Mine: true})
		}
	}
	return result
}
