package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestArtifactAcceptanceIsDirectorOwnedAndSubmissionScoped(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "ao")
	seedProject(t, s, "other")
	now := time.Now().UTC().Truncate(time.Millisecond)
	worker := sampleRecord("ao")
	worker.ID = "worker"
	worker.Kind = domain.KindWorker
	worker.OutputType = domain.SessionOutputArtifact
	worker.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}
	var err error
	if worker, err = s.CreateSession(ctx, worker); err != nil {
		t.Fatal(err)
	}
	director := sampleRecord("ao")
	director.ID = "director"
	director.Kind = domain.KindOrchestrator
	if director, err = s.CreateSession(ctx, director); err != nil {
		t.Fatal(err)
	}
	other := sampleRecord("other")
	other.ID = "other-director"
	other.Kind = domain.KindOrchestrator
	if other, err = s.CreateSession(ctx, other); err != nil {
		t.Fatal(err)
	}
	report := domain.ReportRecord{ID: "done1", SessionID: worker.ID, ProjectID: worker.ProjectID, State: domain.ReportDone, Note: "Complete", CreatedAt: now, AvailableAt: now.Add(time.Minute), SettlementDeadline: now.Add(time.Minute), RepeatCount: 1, DeliveryState: domain.ReportPending}
	if _, err := s.CreateReport(ctx, report); err != nil {
		t.Fatal(err)
	}
	for _, reviewer := range []domain.SessionID{worker.ID, other.ID, "missing"} {
		ok, err := s.AcceptArtifactReport(ctx, report.ID, reviewer, "verified", "test evidence", now.Add(time.Second))
		if err != nil || ok {
			t.Fatalf("unauthorized reviewer %s accepted=%v err=%v", reviewer, ok, err)
		}
	}
	for i := 0; i < 2; i++ {
		ok, err := s.AcceptArtifactReport(ctx, report.ID, director.ID, "verified", "test evidence", now.Add(time.Second))
		if err != nil || !ok {
			t.Fatalf("accept/retry=%v err=%v", ok, err)
		}
	}
	at, ok, err := s.GetArtifactAcceptance(ctx, worker.ID)
	if err != nil || !ok || !at.Equal(now.Add(time.Second)) {
		t.Fatalf("acceptance=%v %v %v", at, ok, err)
	}
	events, err := s.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Type == "session_updated" && e.SessionID == string(worker.ID) {
			found = true
		}
	}
	if !found {
		t.Fatal("acceptance did not invalidate board")
	}
	report.ID = "checkpoint2"
	report.State = domain.ReportCheckpoint
	report.CreatedAt = now.Add(2 * time.Second)
	report.SettlementDeadline = time.Time{}
	if _, err := s.CreateReport(ctx, report); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetArtifactAcceptance(ctx, worker.ID); err != nil || ok {
		t.Fatalf("new work retained acceptance: %v %v", ok, err)
	}
	if ok, err := s.AcceptArtifactReport(ctx, "done1", director.ID, "stale", "evidence", now.Add(3*time.Second)); err != nil || ok {
		t.Fatalf("stale accepted: %v %v", ok, err)
	}
}
