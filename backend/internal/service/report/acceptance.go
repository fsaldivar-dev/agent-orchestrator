package report

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// AcceptInput records the director's evidence-based acceptance of one exact
// worker submission. It is not inferred from a worker's own done claim.
type AcceptInput struct {
	ReportID          string
	ReviewerSessionID domain.SessionID
	Note              string
	Evidence          string
}

type acceptanceWriter interface {
	AcceptArtifactReport(context.Context, string, domain.SessionID, string, string, time.Time) (bool, error)
}

func (s *Service) Accept(ctx context.Context, input AcceptInput) error {
	if strings.TrimSpace(input.ReportID) == "" || input.ReviewerSessionID == "" ||
		strings.TrimSpace(input.Note) == "" || strings.TrimSpace(input.Evidence) == "" ||
		utf8.RuneCountInString(input.Note) > domain.MaxReportTextCharacters || len(input.Evidence) > 8192 {
		return apierr.Invalid("INVALID_ARTIFACT_ACCEPTANCE", "Report, director, review note and evidence are required", nil)
	}
	writer, ok := s.store.(acceptanceWriter)
	if !ok {
		return errors.New("report: acceptance store is required")
	}
	accepted, err := writer.AcceptArtifactReport(ctx, input.ReportID, input.ReviewerSessionID, input.Note, input.Evidence, s.now().UTC())
	if err != nil {
		return err
	}
	if !accepted {
		return apierr.Conflict("ARTIFACT_ACCEPTANCE_STALE", "Requires the latest done artifact report, a settled worker, and a director in the same project", nil)
	}
	return nil
}
