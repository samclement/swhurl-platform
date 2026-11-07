package incident

// Settle removes the front notice after acceptance, or moves a refused copy to the tail until its two-hour
// retry window ends. Retried copies stay in the document at EVERY checkpoint. Platform failures are not settled:
// the caller stops with that notice still pending. Returns whether the copy must be retried.
func (s *State) Settle(sent bool, now float64) bool {
	pending := array(s.document, "pending")
	if len(pending) == 0 {
		return false
	}
	entry := pending[0].(map[string]any)
	if !sent && !has(entry, "about") {
		return false
	}
	s.document["pending"] = pending[1:]
	if !sent && now-number(entry, "queued") < History {
		s.document["pending"] = append(array(s.document, "pending"), entry)
		return true
	}
	history := float64(History)
	if has(entry, "history") {
		history = number(entry, "history")
	}
	object(s.document, "seen")[text(entry, "key")] = clockValue(now + history)
	return false
}
