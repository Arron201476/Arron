package capability

// MatchSnapshot preserves legacy unversioned installations without changing
// their immutable archives. A legacy alias is valid only for the recorded hash.
func (s *SkillPackage) MatchSnapshot(version, contentHash string) (*SkillPackage, bool) {
	if s == nil || (contentHash != "" && s.ContentHash != contentHash) {
		return nil, false
	}
	if s.Version == version {
		return s, true
	}
	if version == "0.0.0" && s.autoVersion && contentHash != "" {
		copy := *s
		copy.Version = version
		return &copy, true
	}
	return nil, false
}
