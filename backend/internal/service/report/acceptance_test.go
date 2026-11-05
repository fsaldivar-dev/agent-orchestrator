package report

import (
	"context"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"testing"
	"time"
)

type acceptanceStore struct {
	fakeStore
	calls int
	allow bool
}

func (s *acceptanceStore) AcceptArtifactReport(context.Context, string, domain.SessionID, string, string, time.Time) (bool, error) {
	s.calls++
	return s.allow, nil
}
func TestArtifactAcceptRequiresEvidenceAndPropagatesConflict(t *testing.T) {
	st := &acceptanceStore{allow: true}
	svc := New(Deps{Store: st})
	input := AcceptInput{ReportID: "r", ReviewerSessionID: "director", Note: "checked", Evidence: "test log"}
	bad := input
	bad.Evidence = " "
	if err := svc.Accept(context.Background(), bad); err == nil || st.calls != 0 {
		t.Fatalf("empty evidence err=%v calls=%d", err, st.calls)
	}
	if err := svc.Accept(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	st.allow = false
	if err := svc.Accept(context.Background(), input); err == nil {
		t.Fatal("stale acceptance was not rejected")
	}
}
