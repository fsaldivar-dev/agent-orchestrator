-- +goose Up
CREATE TABLE artifact_acceptances (
    report_id TEXT PRIMARY KEY REFERENCES reports(id) ON DELETE CASCADE,
    reviewer_session_id TEXT NOT NULL REFERENCES sessions(id),
    note TEXT NOT NULL,
    evidence TEXT NOT NULL,
    accepted_at DATETIME NOT NULL
);
-- +goose StatementBegin
CREATE TRIGGER artifact_acceptances_cdc AFTER INSERT ON artifact_acceptances
BEGIN
 INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
 SELECT r.project_id, r.session_id, 'session_updated', json_object('id', r.session_id), NEW.accepted_at
 FROM reports r WHERE r.id = NEW.report_id;
END;
-- +goose StatementEnd
-- +goose Down
DROP TRIGGER artifact_acceptances_cdc;
DROP TABLE artifact_acceptances;
