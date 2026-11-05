package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// QuotaFallbackPolicy is explicitly opt-in and local-CLI only. An empty source
// model matches that harness's current model; destination models must be named.
// Each role ranks candidates; exhausted choices follow the continuation
// lineage durably, so the selector cannot loop back to an exhausted model.
type QuotaFallbackPolicy struct {
	ExcludedSessionIDs []domain.SessionID                           `json:"excludedSessionIds,omitempty"`
	Enabled            bool                                         `json:"enabled"`
	ProjectIDs         []domain.ProjectID                           `json:"projectIds"`
	Chain              []QuotaFallbackTarget                        `json:"chain"`
	RoleChains         map[domain.SessionKind][]QuotaFallbackTarget `json:"roleChains,omitempty"`
}
type QuotaFallbackTarget struct {
	Harness domain.AgentHarness `json:"harness"`
	Model   string              `json:"model"`
	Effort  string              `json:"effort,omitempty"`
}

type quotaFallbackOperation struct {
	Event            string                `json:"event"`
	State            string                `json:"state"`
	SourceTurn       string                `json:"sourceTurn"`
	SourceBranch     string                `json:"sourceBranch"`
	SourceGeneration string                `json:"sourceGeneration"`
	Source           QuotaFallbackTarget   `json:"source"`
	Target           QuotaFallbackTarget   `json:"target"`
	Checkpoint       string                `json:"checkpoint,omitempty"`
	Reason           string                `json:"reason"`
	Exhausted        []QuotaFallbackTarget `json:"exhausted,omitempty"`
}

// ResumeQuotaFallbacks runs alongside reset-time recovery. No file means off.
// A corrupt policy fails closed; no provider, account or permission is inferred.
func (s *Service) ResumeQuotaFallbacks(ctx context.Context) error {
	if s.dataDir == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(s.dataDir, "quota-fallback.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var policy QuotaFallbackPolicy
	if err := json.Unmarshal(b, &policy); err != nil {
		return fmt.Errorf("quota fallback policy: %w", err)
	}
	return s.RunQuotaFallbacks(ctx, policy)
}

// RunQuotaFallbacks only acts on a settled, explicit quota refusal in the active
// lineage. Controller activity alone, stale quota meters, zero output and HTTP
// 429/network errors are not evidence of exhausted subscription capacity.
func (s *Service) RunQuotaFallbacks(ctx context.Context, policy QuotaFallbackPolicy) error {
	if !policy.Enabled {
		return nil
	}
	if err := validateQuotaFallbackPolicy(policy); err != nil {
		return err
	}
	if s.reader == nil || s.dataDir == "" {
		return errors.New("quota fallback requires durable history and data directory")
	}
	if !s.quotaFallbackMu.TryLock() {
		return nil
	}
	defer s.quotaFallbackMu.Unlock()
	s.mu.RLock()
	ids := make([]domain.SessionID, 0, len(s.controllers))
	for id := range s.controllers {
		ids = append(ids, id)
	}
	s.mu.RUnlock()
	var errs []error
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		record, found, err := s.sessions.GetSession(ctx, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !found || slices.Contains(policy.ExcludedSessionIDs, record.ID) || !quotaFallbackProject(policy, record.ProjectID) || validateUsageLimitSession(record) != nil {
			continue
		}
		c, err := s.Controller(id)
		if err != nil || c.busy() {
			continue
		}
		rows, err := s.reader.LoadConversationSnapshot(ctx, c.ConversationID())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rolePolicy := policy
		if chain, ok := policy.RoleChains[record.Kind]; ok {
			rolePolicy.Chain = chain
		}
		if err := s.runQuotaFallback(ctx, record, c, rows, rolePolicy); err != nil {
			errs = append(errs, fmt.Errorf("quota fallback %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

func validateQuotaFallbackPolicy(p QuotaFallbackPolicy) error {
	if len(p.ProjectIDs) == 0 || len(p.Chain) < 2 {
		return errors.New("quota fallback requires explicit projects and at least two targets")
	}
	for role, chain := range p.RoleChains {
		if role != domain.KindWorker && role != domain.KindOrchestrator {
			return errors.New("unknown quota fallback role")
		}
		if err := validateQuotaFallbackPolicy(QuotaFallbackPolicy{ProjectIDs: p.ProjectIDs, Chain: chain}); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for i, target := range p.Chain {
		if target.Harness != domain.HarnessClaudeCode && target.Harness != domain.HarnessCodex && target.Harness != domain.HarnessKimi {
			return errors.New("quota fallback supports only existing Claude Code, Codex and Kimi CLIs")
		}
		if i > 0 && strings.TrimSpace(target.Model) == "" {
			return errors.New("quota fallback destination model must be explicit")
		}
		key := string(target.Harness) + "/" + target.Model
		if seen[key] {
			return errors.New("quota fallback chain contains a duplicate target")
		}
		seen[key] = true
	}
	return nil
}
func quotaFallbackProject(p QuotaFallbackPolicy, id domain.ProjectID) bool {
	for _, wanted := range p.ProjectIDs {
		if wanted == id {
			return true
		}
	}
	return false
}
func quotaTargetExhausted(target QuotaFallbackTarget, tried []QuotaFallbackTarget) bool {
	for _, previous := range tried {
		if previous.Harness == target.Harness && previous.Model == target.Model {
			return true
		}
	}
	return false
}
func quotaFallbackID(turn string) string {
	sum := sha256.Sum256([]byte(turn))
	return "quota-fallback-" + hex.EncodeToString(sum[:16])
}
func quotaFallbackLatest(rows ConversationRows) (domain.ConversationTurn, bool) {
	var latest domain.ConversationTurn
	for _, t := range rows.Turns {
		if t.RolledBackAt != nil {
			continue
		}
		// A queued/running/newer turn is work supplied after the failure. Never
		// replace it with an automatic continuation of the older request.
		if latest.ID == "" || t.RequestedAt.After(latest.RequestedAt) || t.RequestedAt.Equal(latest.RequestedAt) {
			latest = t
		}
	}
	return latest, latest.ID != ""
}
func quotaFallbackRead(rows ConversationRows, id string) (quotaFallbackOperation, bool) {
	for _, a := range rows.Activities {
		if a.ID == id || a.ProviderItemID == id {
			var op quotaFallbackOperation
			if json.Unmarshal(a.Detail, &op) == nil && op.Event == "quota_fallback" {
				return op, true
			}
		}
	}
	return quotaFallbackOperation{}, false
}

func (s *Service) runQuotaFallback(ctx context.Context, record domain.SessionRecord, c *Controller, rows ConversationRows, policy QuotaFallbackPolicy) error {
	source, ok := quotaFallbackLatest(rows)
	// The provider boundary may hide the failed source turn from the active
	// timeline. The durable outbox, not visible timeline membership, owns crash
	// recovery between a committed switch and continuation delivery.
	for _, activity := range rows.Activities {
		var pending quotaFallbackOperation
		if json.Unmarshal(activity.Detail, &pending) != nil || pending.Event != "quota_fallback" || (pending.State != "prepared" && pending.State != "switched") {
			continue
		}
		delivered, err := s.messageDelivered(ctx, record.ID, quotaFallbackID(pending.SourceTurn))
		if err != nil {
			return err
		}
		if delivered {
			pending.State = "sent"
			if err := s.recordQuotaFallback(ctx, rows.Conversation.ID, quotaFallbackID(pending.SourceTurn), pending); err != nil {
				return err
			}
			continue
		}
		failed, err := s.store.TurnByID(ctx, pending.SourceTurn)
		if err != nil {
			return err
		}
		if ok && source.ID != failed.ID && !source.RequestedAt.Before(failed.RequestedAt) {
			pending.State, pending.Reason = "held", "New work superseded the pending automatic continuation."
			return s.recordQuotaFallback(ctx, rows.Conversation.ID, quotaFallbackID(pending.SourceTurn), pending)
		}
		source, ok = failed, true
		break
	}
	if !ok || source.State != domain.TurnStateFailed || source.UsageLimit == nil || source.HandledBySessionID != record.ID || source.ProviderTurnID == "" || source.UsageLimit.AutoResume {
		return nil
	}
	if pending, err := s.store.HasPendingConversationInteractions(ctx, rows.Conversation.ID); err != nil {
		return err
	} else if pending {
		return nil
	}
	id := quotaFallbackID(source.ID)
	op, exists := quotaFallbackRead(rows, id)
	if exists && (op.State == "sent" || op.State == "held") {
		return nil
	}
	if !exists {
		if source.BranchID != rows.ActiveBranch.ID {
			return nil
		}
		current := QuotaFallbackTarget{Harness: record.Harness, Model: c.Settings().Model, Effort: c.Settings().ReasoningEffort}
		exhausted := []QuotaFallbackTarget{current}
		// Carry exhausted choices across continuation turns, including a
		// provider-resolved alias. A manually selected starting model does not
		// exclude higher-ranked, still-available candidates.
		for _, message := range rows.Messages {
			if message.TurnID != source.ID || message.ClientMessageID == "" {
				continue
			}
			previous, found := quotaFallbackRead(rows, message.ClientMessageID)
			if found && previous.State == "sent" {
				exhausted = append(exhausted, previous.Exhausted...)
				exhausted = append(exhausted, previous.Target)
			}
		}
		op = quotaFallbackOperation{Event: "quota_fallback", State: "prepared", SourceTurn: source.ID, SourceBranch: rows.ActiveBranch.ID, SourceGeneration: c.Generation(), Source: current, Reason: source.ErrorMessage, Exhausted: exhausted}
		// Skip unavailable local CLIs before touching the source. No account
		// creation, paid API route or permission escalation is a fallback.
		ready := false
		for _, target := range policy.Chain[1:] {
			if quotaTargetExhausted(target, exhausted) {
				continue
			}
			op.Target = target
			if reason := s.quotaFallbackCapacity(ctx, record.ID, target); reason != "" {
				op.Reason = reason
				continue
			}
			if target.Harness != record.Harness {
				permissions, _ := s.switchPermissions(ctx, c, domain.ProviderSwitchRequest{TargetHarness: target.Harness})
				if permissions == ports.PermissionModeBypassPermissions && s.sourcePermissions(c) != ports.PermissionModeBypassPermissions {
					op.Reason = "Target requires broader permissions than the source"
					continue
				}
				if err := s.PreflightChat(ctx, target.Harness, permissions); err != nil {
					op.Reason = "Target CLI unavailable: " + err.Error()
					continue
				}
			}
			ready = true
			break
		}
		if !ready {
			op.State = "held"
			if op.Reason == source.ErrorMessage {
				op.Reason = "All configured alternatives have been exhausted; waiting for quota or a manual model choice."
			}
			return s.recordQuotaFallback(ctx, rows.Conversation.ID, id, op)
		}
		checkpoint, err := s.writeQuotaCheckpoint(id, record, rows)
		if err != nil {
			return err
		}
		op.Checkpoint = checkpoint
		op.Reason = fmt.Sprintf("Explicit quota failure; selected %s/%s for role %s after readiness and quota checks. Source: %s", op.Target.Harness, op.Target.Model, record.Kind, source.ErrorMessage)
		if err := s.recordQuotaFallback(ctx, rows.Conversation.ID, id, op); err != nil {
			return err
		}
	}
	// The normal provider-switch gate also serializes this with manual switches.
	unlock, err := s.lockProviderSwitch(ctx, record.ID)
	if err != nil {
		return err
	}
	defer unlock()
	fresh, found, err := s.sessions.GetSession(ctx, record.ID)
	if err != nil {
		return err
	}
	if !found || validateUsageLimitSession(fresh) != nil {
		return nil
	}
	live, err := s.Controller(record.ID)
	if err != nil || live.busy() {
		return nil
	}
	latestRows, err := s.reader.LoadConversationSnapshot(ctx, rows.Conversation.ID)
	if err != nil {
		return err
	}
	latest, ok := quotaFallbackLatest(latestRows)
	if ok && latest.ID != source.ID && !latest.RequestedAt.Before(source.RequestedAt) {
		return nil
	}
	if pending, err := s.store.HasPendingConversationInteractions(ctx, rows.Conversation.ID); err != nil {
		return err
	} else if pending {
		return nil
	}
	// A durable switch marker proves the crash window between switching and
	// recording 'switched'. A manual switch is never silently switched back.
	targetActive := fresh.Harness == op.Target.Harness && live.Settings().Model == op.Target.Model
	switchedByUs := false
	for _, a := range latestRows.Activities {
		if a.RequestID == id {
			var detail providerSwitchActivityDetail
			if json.Unmarshal(a.Detail, &detail) == nil && detail.Event == "provider_switch" {
				switchedByUs = true
			}
		}
	}
	sameControllerPick := op.Target.Harness == op.Source.Harness && live.Generation() == op.SourceGeneration && latestRows.ActiveBranch.ID == op.SourceBranch
	if targetActive && (op.State == "switched" || switchedByUs || sameControllerPick) {
		// Resume the durable outbox below, without starting another provider.
	} else {
		if fresh.Harness != op.Source.Harness || live.Generation() != op.SourceGeneration || live.Settings().Model != op.Source.Model || latestRows.ActiveBranch.ID != op.SourceBranch {
			op.State, op.Reason = "held", "Session changed since the quota failure; automatic handoff cancelled."
			return s.recordQuotaFallback(ctx, rows.Conversation.ID, id, op)
		}
		if op.Target.Harness == op.Source.Harness {
			err = s.applyPicksToActiveTarget(ctx, record.ID, domain.ProviderSwitchRequest{TargetHarness: op.Target.Harness, Model: op.Target.Model, Effort: op.Target.Effort})
		} else {
			err = s.switchProvider(ctx, record.ID, domain.ProviderSwitchRequest{TargetHarness: op.Target.Harness, Model: op.Target.Model, Effort: op.Target.Effort, IdempotencyKey: id})
		}
		if err != nil {
			op.State, op.Reason = "held", "Automatic handoff stopped: "+err.Error()
			return s.recordQuotaFallback(ctx, rows.Conversation.ID, id, op)
		}
	}
	op.State = "switched"
	if err := s.recordQuotaFallback(ctx, rows.Conversation.ID, id, op); err != nil {
		return err
	}
	text := fmt.Sprintf("The previous model stopped because its subscription quota was exhausted. Continue the unfinished task in this SAME workspace. First read the complete local checkpoint at %s; it contains the failed request, conversation, plan and partial output. Preserve all existing files and uncommitted changes. Check what already ran before repeating any tool action. Earlier completed requests are history, not new tasks. Keep the user's scope and permissions. Use the existing AO session history for older context. Do not treat quota exhaustion as successful completion.", op.Checkpoint)
	_, err = s.sendQuotaContinuation(ctx, record.ID, source, ports.ChatUserMessage{Text: text, ClientMessageID: id, Origin: domain.MessageOriginAutomation})
	if err != nil {
		// A send may have been durably accepted before its provider errored. Never
		// enqueue it twice or switch backwards on a subsequent sweep.
		delivered, readErr := s.messageDelivered(ctx, record.ID, id)
		if readErr != nil {
			return readErr
		}
		if !delivered {
			op.State, op.Reason = "held", "Continuation could not be delivered: "+err.Error()
			return s.recordQuotaFallback(ctx, rows.Conversation.ID, id, op)
		}
	}
	op.State = "sent"
	return s.recordQuotaFallback(ctx, rows.Conversation.ID, id, op)
}

func (s *Service) recordQuotaFallback(ctx context.Context, conversation, id string, op quotaFallbackOperation) error {
	detail, err := json.Marshal(op)
	if err != nil {
		return err
	}
	summary := "Quota fallback: " + op.State + " · " + string(op.Source.Harness) + " → " + string(op.Target.Harness) + " / " + op.Target.Model
	if op.State == "held" {
		summary = "Quota fallback paused: " + op.Reason
	}
	return s.store.UpsertActivity(ctx, conversation, "", domain.ConversationActivity{ID: id, ProviderItemID: id, Kind: domain.ActivityKindSystem, Status: domain.ActivityStatusCompleted, Summary: summary, Detail: detail}, s.now())
}
func (s *Service) writeQuotaCheckpoint(id string, record domain.SessionRecord, rows ConversationRows) (string, error) {
	dir := filepath.Join(s.dataDir, "quota-fallback", "checkpoints")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	// No git writes, stash, reset, worktree copy or commit: the next native CLI
	// inherits the exact registered workspace. Keep all canonical context locally.
	data, err := json.MarshalIndent(struct {
		Session domain.SessionRecord
		History ConversationRows
		SavedAt time.Time
	}{record, rows, s.now()}, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, id+".json")
	f, err := os.CreateTemp(dir, ".checkpoint-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

// A missing/stale meter does not mean either exhausted or available. In that
// case the native CLI gets one bounded attempt and its settled result decides.
func (s *Service) quotaFallbackCapacity(ctx context.Context, id domain.SessionID, target QuotaFallbackTarget) string {
	s.mu.RLock()
	accounts := s.usageLimitAccounts
	s.mu.RUnlock()
	if accounts == nil {
		return ""
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, limits, err := accounts.SessionLimits(probeCtx, id, target.Harness, true)
	if err != nil || limits.FetchedAt.IsZero() || s.now().Sub(limits.FetchedAt) > 30*time.Second || limits.FetchedAt.Sub(s.now()) > 30*time.Second {
		return ""
	}
	for _, window := range limits.Windows {
		if window.UsedPercent < 100 || (window.ResetsAt != nil && !window.ResetsAt.After(s.now())) {
			continue
		}
		global := window.ID == "five_hour" || window.ID == "seven_day"
		if target.Harness == domain.HarnessCodex {
			global = !strings.Contains(window.Label, " · ")
		}
		model := strings.TrimPrefix(window.ID, "model:")
		scoped := strings.HasPrefix(window.ID, "model:") && strings.Contains(strings.ToLower(target.Model), strings.ToLower(model))
		scoped = scoped || window.ID == "seven_day_opus" && strings.Contains(target.Model, "opus") || window.ID == "seven_day_sonnet" && strings.Contains(target.Model, "sonnet")
		if global || scoped {
			reset := "an unknown reset time"
			if window.ResetsAt != nil {
				reset = window.ResetsAt.Format(time.RFC3339)
			}
			return fmt.Sprintf("Skipped %s/%s: %s quota is exhausted until %s", target.Harness, target.Model, window.Label, reset)
		}
	}
	return ""
}

// Use the controller's admission lock for the final read and delivery. A human
// message arriving during provider startup wins; fallback must not queue stale
// instructions behind it.
func (s *Service) sendQuotaContinuation(ctx context.Context, id domain.SessionID, source domain.ConversationTurn, msg ports.ChatUserMessage) (domain.ConversationTurn, error) {
	c, release, err := s.workingController(ctx, id)
	if err != nil {
		return domain.ConversationTurn{}, err
	}
	defer release()
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.busy() {
		return domain.ConversationTurn{}, ErrTurnRunning
	}
	if _, err := s.store.NextQueuedTurn(ctx, c.ConversationID()); err == nil {
		return domain.ConversationTurn{}, errors.New("new queued work superseded quota continuation")
	} else if !errors.Is(err, domain.ErrNoQueuedTurn) {
		return domain.ConversationTurn{}, err
	}
	rows, err := s.reader.LoadConversationSnapshot(ctx, c.ConversationID())
	if err != nil {
		return domain.ConversationTurn{}, err
	}
	if latest, ok := quotaFallbackLatest(rows); ok && latest.ID != source.ID && !latest.RequestedAt.Before(source.RequestedAt) {
		return domain.ConversationTurn{}, errors.New("new work superseded quota continuation")
	}
	if pending, err := s.store.HasPendingConversationInteractions(ctx, c.ConversationID()); err != nil {
		return domain.ConversationTurn{}, err
	} else if pending {
		return domain.ConversationTurn{}, errors.New("pending interaction blocks quota continuation")
	}
	return c.sendLocked(ctx, msg, false, nil, nil)
}
