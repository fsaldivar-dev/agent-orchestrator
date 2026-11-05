-- name: CreateReport :one
INSERT INTO reports (
    id, session_id, project_id, state, note, message, created_at, available_at,
    settlement_deadline, repeat_count
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: CreateReportOutput :exec
INSERT INTO report_outputs (report_id, position, kind, reference, label)
VALUES (?, ?, ?, ?, ?);

-- name: GetReport :one
SELECT * FROM reports WHERE id = ?;

-- name: ListReportOutputs :many
SELECT * FROM report_outputs WHERE report_id = ? ORDER BY position;

-- name: ListReportsBySession :many
SELECT * FROM reports WHERE session_id = ? ORDER BY created_at, id;

-- name: ListReportedPRURLs :many
SELECT o.reference FROM report_outputs o
JOIN reports r ON r.id = o.report_id
WHERE r.session_id = ? AND o.kind = 'pr_created'
ORDER BY r.created_at, r.id, o.position;

-- name: ListReportsByProject :many
SELECT * FROM reports WHERE project_id = ? ORDER BY created_at, id;

-- name: ListPendingReportSchedule :many
SELECT * FROM reports
WHERE delivery_state = 'pending'
ORDER BY available_at, created_at, id
LIMIT ?;

-- name: ListPendingReportsByProjectSchedule :many
SELECT * FROM reports
WHERE project_id = ? AND delivery_state = 'pending'
ORDER BY available_at, created_at, id
LIMIT ?;

-- name: AssignPendingReportsToBatch :exec
UPDATE reports
SET delivery_batch_id = sqlc.arg(delivery_batch_id)
WHERE project_id = sqlc.arg(project_id) AND delivery_state = 'pending'
  AND delivery_batch_id = '';

-- name: ClaimPendingReportsByBatch :many
UPDATE reports
SET delivery_state = 'claimed', claim_token = sqlc.arg(claim_token),
    claimed_at = sqlc.arg(claimed_at), delivery_attempts = delivery_attempts + 1,
    last_error = ''
WHERE project_id = sqlc.arg(project_id) AND delivery_state = 'pending'
  AND delivery_batch_id = sqlc.arg(delivery_batch_id)
RETURNING *;

-- name: AcknowledgeReportBatch :many
UPDATE reports
SET delivery_state = 'acknowledged', acknowledged_at = sqlc.arg(acknowledged_at)
WHERE project_id = sqlc.arg(project_id) AND delivery_state = 'claimed'
  AND claim_token = sqlc.arg(claim_token)
RETURNING *;

-- name: ReleaseReportBatch :many
UPDATE reports
SET delivery_state = 'pending', available_at = min(available_at, sqlc.arg(available_at)),
    claim_token = '', claimed_at = NULL, last_error = sqlc.arg(last_error)
WHERE project_id = sqlc.arg(project_id) AND delivery_state = 'claimed'
  AND claim_token = sqlc.arg(claim_token)
RETURNING *;

-- name: DeferReportBatch :many
UPDATE reports
SET delivery_state = 'pending', available_at = max(available_at, sqlc.arg(available_at)),
    claim_token = '', claimed_at = NULL, last_error = sqlc.arg(last_error)
WHERE project_id = sqlc.arg(project_id) AND delivery_state = 'claimed'
  AND claim_token = sqlc.arg(claim_token)
RETURNING *;

-- name: GetReportInterrupt :one
SELECT last_interrupted_at FROM report_worker_interrupts WHERE session_id = ?;

-- name: PutReportInterrupt :exec
INSERT INTO report_worker_interrupts (session_id, last_interrupted_at)
VALUES (?, ?)
ON CONFLICT(session_id) DO UPDATE SET last_interrupted_at = excluded.last_interrupted_at;

-- name: ListPendingReports :many
SELECT * FROM reports
WHERE delivery_state = 'pending' AND available_at <= ?
ORDER BY created_at, id
LIMIT ?;

-- name: ClaimReport :one
UPDATE reports
SET delivery_state = 'claimed', claim_token = sqlc.arg(claim_token),
    claimed_at = sqlc.arg(claimed_at), delivery_attempts = delivery_attempts + 1,
    last_error = ''
WHERE id = sqlc.arg(id) AND delivery_state = 'pending' AND available_at <= sqlc.arg(claimed_at)
RETURNING *;

-- name: AcknowledgeReport :one
UPDATE reports
SET delivery_state = 'acknowledged', acknowledged_at = sqlc.arg(acknowledged_at)
WHERE id = sqlc.arg(id) AND delivery_state = 'claimed' AND claim_token = sqlc.arg(claim_token)
RETURNING *;

-- name: ReleaseReport :one
UPDATE reports
SET delivery_state = 'pending', available_at = sqlc.arg(available_at),
    claim_token = '', claimed_at = NULL, last_error = sqlc.arg(last_error)
WHERE id = sqlc.arg(id) AND delivery_state = 'claimed' AND claim_token = sqlc.arg(claim_token)
RETURNING *;

-- name: RequeueClaimedReports :execrows
UPDATE reports
SET delivery_state = 'pending', claim_token = '', claimed_at = NULL,
    last_error = 'delivery claim recovered after daemon restart'
WHERE delivery_state = 'claimed';

-- name: AcceptArtifactReport :execrows
INSERT INTO artifact_acceptances (report_id, reviewer_session_id, note, evidence, accepted_at)
SELECT r.id, reviewer.id, sqlc.arg(note), sqlc.arg(evidence), sqlc.arg(accepted_at)
FROM reports r JOIN sessions worker ON worker.id = r.session_id
JOIN sessions reviewer ON reviewer.id = sqlc.arg(reviewer_session_id)
WHERE r.id = sqlc.arg(report_id) AND r.state = 'done'
AND worker.kind = 'worker' AND worker.is_terminated = FALSE
AND worker.activity_state IN ('idle', 'exited')
AND worker.session_output_type IN ('artifact', 'pr_artifact')
AND reviewer.kind = 'orchestrator' AND reviewer.is_terminated = FALSE
AND reviewer.project_id = worker.project_id
AND NOT EXISTS (SELECT 1 FROM reports newer WHERE newer.session_id = r.session_id
 AND (newer.created_at > r.created_at OR (newer.created_at = r.created_at AND newer.id > r.id)))
ON CONFLICT(report_id) DO UPDATE SET report_id = excluded.report_id;

-- name: GetArtifactAcceptance :one
SELECT a.accepted_at FROM artifact_acceptances a
JOIN reports r ON r.id = a.report_id
WHERE r.session_id = ? AND r.state = 'done'
AND NOT EXISTS (SELECT 1 FROM reports newer WHERE newer.session_id = r.session_id
 AND (newer.created_at > r.created_at OR (newer.created_at = r.created_at AND newer.id > r.id)))
ORDER BY a.accepted_at DESC LIMIT 1;

-- name: ListArtifactAcceptances :many
SELECT r.session_id, a.accepted_at FROM artifact_acceptances a
JOIN reports r ON r.id = a.report_id
WHERE r.session_id IN (SELECT value FROM json_each(?)) AND r.state = 'done'
AND NOT EXISTS (SELECT 1 FROM reports newer WHERE newer.session_id = r.session_id
 AND (newer.created_at > r.created_at OR (newer.created_at = r.created_at AND newer.id > r.id)));
