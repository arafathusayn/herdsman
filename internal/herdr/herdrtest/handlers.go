package herdrtest

// Handler returns the fixture's own handler of a method (agent.get,
// agent.list, pane.list, pane.read, notification.show), so a test can wrap
// it to act between calls; nil for another method.
func (f *Fixture) Handler(method string) Handler {
	switch method {
	case "agent.get":
		return f.agentGet
	case "agent.list":
		return f.agentList
	case "pane.list":
		return f.paneList
	case "pane.read":
		return f.paneRead
	case "notification.show":
		return f.notify
	}
	return nil
}

// ResetRequests forgets the recorded requests, so a long test can measure
// its own memory without the fake's log.
func (s *Server) ResetRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}
