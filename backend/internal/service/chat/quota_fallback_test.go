package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

func fallbackPolicy() chatsvc.QuotaFallbackPolicy {
	return chatsvc.QuotaFallbackPolicy{Enabled: true, ProjectIDs: []domain.ProjectID{testProject}, Chain: []chatsvc.QuotaFallbackTarget{{Harness: domain.HarnessCodex}, {Harness: domain.HarnessClaudeCode, Model: "sonnet"}}}
}
func fallbackMarker(t *testing.T, h *harness) map[string]any {
	t.Helper()
	c, err := h.st.ConversationForSession(context.Background(), testSession)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := h.st.LoadConversationSnapshot(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range rows.Activities {
		var detail map[string]any
		if json.Unmarshal(a.Detail, &detail) == nil && detail["event"] == "quota_fallback" {
			return detail
		}
	}
	t.Fatal("missing fallback audit activity")
	return nil
}
func TestQuotaFallbackPreservesHistoryAndDeliversOnce(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	source := failUsageLimitedTurn(t, h, timedUsageLimit)
	ctx := context.Background()
	before, _, _ := h.st.GetSession(ctx, testSession)
	// Simultaneous scheduler ticks must result in only one replacement.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := h.svc.RunQuotaFallbacks(ctx, fallbackPolicy()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	after, _, _ := h.st.GetSession(ctx, testSession)
	if after.Harness != domain.HarnessClaudeCode || after.ID != before.ID || after.Metadata.WorkspacePath != before.Metadata.WorkspacePath || after.Metadata.Branch != before.Metadata.Branch {
		t.Fatalf("workspace/session identity changed: before=%+v after=%+v", before, after)
	}
	marker := fallbackMarker(t, h)
	if marker["state"] != "sent" {
		t.Fatalf("marker = %+v", marker)
	}
	b, err := os.ReadFile(marker["checkpoint"].(string))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{source.ID, "finish this task", timedUsageLimit} {
		if !strings.Contains(string(b), expected) {
			t.Fatalf("checkpoint missing %q", expected)
		}
	}
	driver.mu.Lock()
	sent := driver.fresh.sentMessages()
	calls := driver.startCalls
	driver.mu.Unlock()
	if calls != 2 || len(sent) != 1 || !strings.Contains(sent[0].Text, "SAME workspace") {
		t.Fatalf("calls=%d sent=%+v", calls, sent)
	}
	if err := h.svc.RunQuotaFallbacks(ctx, fallbackPolicy()); err != nil {
		t.Fatal(err)
	}
	if len(driver.fresh.sentMessages()) != 1 {
		t.Fatal("duplicated continuation")
	}
}
func TestQuotaFallbackIgnoresUnrelatedFailuresAndOtherProjects(t *testing.T) {
	for _, tc := range []struct {
		name, message          string
		disabled, otherProject bool
	}{
		{name: "network", message: "connection reset by peer"},
		{name: "generic429", message: "HTTP 429: too many requests"},
		{name: "off", message: timedUsageLimit, disabled: true},
		{name: "scope", message: timedUsageLimit, otherProject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, driver := newEditHarness(t, false)
			failUsageLimitedTurn(t, h, tc.message)
			p := fallbackPolicy()
			p.Enabled = !tc.disabled
			if tc.otherProject {
				p.ProjectIDs = []domain.ProjectID{"different-project"}
			}
			if err := h.svc.RunQuotaFallbacks(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			driver.mu.Lock()
			defer driver.mu.Unlock()
			if driver.startCalls != 1 {
				t.Fatal("unexpected switch")
			}
		})
	}
}
func TestQuotaFallbackSameCLINextModel(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	failUsageLimitedTurn(t, h, timedUsageLimit)
	p := fallbackPolicy()
	p.Chain[1] = chatsvc.QuotaFallbackTarget{Harness: domain.HarnessCodex, Model: "next-model"}
	if err := h.svc.RunQuotaFallbacks(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	c, err := h.svc.Controller(testSession)
	if err != nil {
		t.Fatal(err)
	}
	if c.Settings().Model != "next-model" {
		t.Fatalf("model=%s", c.Settings().Model)
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.startCalls != 1 {
		t.Fatal("same CLI unnecessarily restarted")
	}
	if fallbackMarker(t, h)["state"] != "sent" {
		t.Fatal("not sent")
	}
}
func TestQuotaFallbackHoldsWhenChainExhausted(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	failUsageLimitedTurn(t, h, timedUsageLimit)
	p := fallbackPolicy()
	p.Chain = []chatsvc.QuotaFallbackTarget{{Harness: domain.HarnessClaudeCode, Model: "sonnet"}, {Harness: domain.HarnessCodex, Model: "current"}}
	if err := h.ctrl.SetSettings(context.Background(), domain.ConversationSettings{Model: "current"}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RunQuotaFallbacks(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if fallbackMarker(t, h)["state"] != "held" {
		t.Fatal("must hold at end of chain")
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.startCalls != 1 {
		t.Fatal("looped back")
	}
}
func TestQuotaFallbackFailedTargetKeepsSource(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	failUsageLimitedTurn(t, h, timedUsageLimit)
	driver.mu.Lock()
	driver.startErr = errors.New("target could not start")
	driver.mu.Unlock()
	// The normal switch attempts source restore. Regardless of readiness, no
	// second automatic attempt may destroy or duplicate work.
	if err := h.svc.RunQuotaFallbacks(context.Background(), fallbackPolicy()); err != nil {
		t.Fatal(err)
	}
	if fallbackMarker(t, h)["state"] != "held" {
		t.Fatal("failed target must hold")
	}
	if err := h.svc.RunQuotaFallbacks(context.Background(), fallbackPolicy()); err != nil {
		t.Fatal(err)
	}
}
func TestQuotaFallbackRejectsUnapprovedProvider(t *testing.T) {
	h, _, _ := newEditHarness(t, false)
	p := fallbackPolicy()
	p.Chain[1].Harness = domain.HarnessOpenCode
	if err := h.svc.RunQuotaFallbacks(context.Background(), p); err == nil {
		t.Fatal("unapproved provider accepted")
	}
}

type failFallbackCheckpointStore struct {
	chatsvc.Store
	fail bool
}

func (s *failFallbackCheckpointStore) UpsertActivity(ctx context.Context, conversation, turn string, activity domain.ConversationActivity, now time.Time) error {
	var op map[string]any
	if s.fail && json.Unmarshal(activity.Detail, &op) == nil && op["event"] == "quota_fallback" && op["state"] == "switched" {
		s.fail = false
		return errors.New("simulated crash after provider commit")
	}
	return s.Store.UpsertActivity(ctx, conversation, turn, activity, now)
}
func TestQuotaFallbackRecoversCommittedSwitchWithoutDuplicate(t *testing.T) {
	var wrapped *failFallbackCheckpointStore
	h, _, driver := newEditHarnessWithStore(t, false, func(st *store.Store) chatsvc.Store {
		wrapped = &failFallbackCheckpointStore{Store: st, fail: true}
		return wrapped
	})
	failUsageLimitedTurn(t, h, timedUsageLimit)
	if err := h.svc.RunQuotaFallbacks(context.Background(), fallbackPolicy()); err == nil {
		t.Fatal("expected interrupted outbox")
	}
	if err := h.svc.RunQuotaFallbacks(context.Background(), fallbackPolicy()); err != nil {
		t.Fatal(err)
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.startCalls != 2 || len(driver.fresh.sentMessages()) != 1 {
		t.Fatalf("duplicate or missing continuation: starts=%d messages=%d marker=%+v", driver.startCalls, len(driver.fresh.sentMessages()), fallbackMarker(t, h))
	}
}
func TestQuotaFallbackDoesNotOverrideNewWork(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	failUsageLimitedTurn(t, h, timedUsageLimit)
	if _, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{Text: "new instructions", ClientMessageID: "new-work"}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RunQuotaFallbacks(context.Background(), fallbackPolicy()); err != nil {
		t.Fatal(err)
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.startCalls != 1 {
		t.Fatal("overrode new work")
	}
}
func TestQuotaFallbackLeavesWorkspaceBytesUntouched(t *testing.T) {
	h, _, _ := newEditHarness(t, false)
	rec, _, _ := h.st.GetSession(context.Background(), testSession)
	path := filepath.Join(rec.Metadata.WorkspacePath, "pending-work.txt")
	if err := os.WriteFile(path, []byte("unfinished user work\x00"), 0600); err != nil {
		t.Fatal(err)
	}
	failUsageLimitedTurn(t, h, timedUsageLimit)
	if err := h.svc.RunQuotaFallbacks(context.Background(), fallbackPolicy()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "unfinished user work\x00" {
		t.Fatalf("workspace changed: %q %v", b, err)
	}
}
func TestQuotaFallbackRolePolicyAndFreshCapacity(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	failUsageLimitedTurn(t, h, timedUsageLimit)
	now := h.now()
	reset := now.Add(time.Hour)
	h.svc.SetUsageLimitAccounts(usageLimitAccountReaderFunc(func(context.Context, domain.SessionID, domain.AgentHarness, bool) (string, domain.AgentAccountLimits, error) {
		return "local", domain.AgentAccountLimits{FetchedAt: now, Windows: []domain.AgentAccountLimitWindow{{ID: "seven_day", Label: "Weekly", UsedPercent: 100, ResetsAt: &reset}}}, nil
	}))
	p := fallbackPolicy()
	// Both Claude candidates share a globally exhausted weekly pool; neither
	// should be launched even though the models are listed in the catalog.
	p.RoleChains = map[domain.SessionKind][]chatsvc.QuotaFallbackTarget{domain.KindOrchestrator: {
		{Harness: domain.HarnessCodex}, {Harness: domain.HarnessClaudeCode, Model: "opus"}, {Harness: domain.HarnessClaudeCode, Model: "sonnet"},
	}}
	if err := h.svc.RunQuotaFallbacks(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if fallbackMarker(t, h)["state"] != "held" {
		t.Fatal("exhausted account should hold")
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.startCalls != 1 {
		t.Fatal("started exhausted candidate")
	}
}

func TestQuotaFallbackAdvancesAcrossProvidersAndStops(t *testing.T) {
	h, _, driver := newEditHarness(t, false)
	failUsageLimitedTurn(t, h, timedUsageLimit)
	p := fallbackPolicy()
	p.Chain = append(p.Chain, chatsvc.QuotaFallbackTarget{Harness: domain.HarnessCodex, Model: "other"})
	ctx := context.Background()
	for attempt := 0; attempt < 3; attempt++ {
		if err := h.svc.RunQuotaFallbacks(ctx, p); err != nil {
			t.Fatal(err)
		}
		if attempt == 2 {
			break
		}
		c, err := h.svc.Controller(testSession)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := h.st.LoadConversationSnapshot(ctx, c.ConversationID())
		if err != nil {
			t.Fatal(err)
		}
		turn := rows.Turns[len(rows.Turns)-1]
		driver.mu.Lock()
		conv := driver.fresh
		driver.mu.Unlock()
		conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn.ProviderTurnID, TurnState: domain.TurnStateFailed, Err: errors.New(timedUsageLimit)})
		failedTurnSnapshot(t, h, turn.ID)
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.startCalls != 3 {
		t.Fatalf("wanted two handoffs then hold, got %d starts", driver.startCalls)
	}
}
