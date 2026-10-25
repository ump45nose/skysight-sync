package syncer

import (
	"path/filepath"
	"sort"
	"time"
)

// Diagnostics contain identifiers and validation reasons, never summary text.
type FileIssue struct {
	Device    string    `json:"device"`
	Filename  string    `json:"filename"`
	Digest    string    `json:"sha256,omitempty"`
	Reason    string    `json:"reason"`
	Recovered bool      `json:"metadata_recovered"`
	Detected  time.Time `json:"detected_at"`
}

func (e *Engine) noteIssue(s *State, report *Report, device, name, reason string, raw []byte, recovered, dry bool) error {
	issue := FileIssue{Device: device, Filename: name, Reason: reason, Recovered: recovered, Detected: e.now()}
	if raw != nil {
		issue.Digest = Digest(raw)
		if !recovered && !dry {
			// Copy the exact failed revision; never move, rewrite or delete the source.
			entry := Entry{Name: name, Digest: issue.Digest, Size: int64(len(raw))}
			if err := archive(filepath.Join(e.Root, "issues"), device, entry, raw); err != nil {
				return err
			}
		}
	}
	if s.Issues == nil {
		s.Issues = map[string]FileIssue{}
	}
	s.Issues[device+"/"+name] = issue
	report.Issues = append(report.Issues, issue)
	return nil
}

func unresolvedIssues(s State) bool {
	for _, issue := range s.Issues {
		if !issue.Recovered {
			return true
		}
	}
	return false
}

func sortedIssues(s State) []FileIssue {
	issues := make([]FileIssue, 0, len(s.Issues))
	for _, issue := range s.Issues {
		issues = append(issues, issue)
	}
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].Device+"/"+issues[i].Filename < issues[j].Device+"/"+issues[j].Filename
	})
	return issues
}
