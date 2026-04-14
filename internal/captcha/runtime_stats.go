package captcha

type RuntimeStatsReporter interface {
	RuntimeStats() RuntimeStats
}

type RuntimeStats struct {
	Successes uint64
	Failures  uint64
	Timeouts  uint64
}

func (s *Server) RuntimeStats() RuntimeStats {
	if s == nil {
		return RuntimeStats{}
	}

	s.statsMu.RLock()
	defer s.statsMu.RUnlock()
	return s.stats
}

func (s *Server) recordSuccess() {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	s.stats.Successes++
}

func (s *Server) recordFailure() {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	s.stats.Failures++
}

func (s *Server) recordTimeout() {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	s.stats.Timeouts++
}
