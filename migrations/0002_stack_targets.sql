-- Rules can target a single container or a whole Compose project (stack).
-- Existing rules keep their container reference and become CONTAINER rules.

ALTER TABLE container_rules RENAME COLUMN container_id TO target_ref;
ALTER TABLE container_rules RENAME COLUMN container_name TO target_name;

ALTER TABLE container_rules ADD COLUMN target_type TEXT NOT NULL DEFAULT 'CONTAINER'
    CHECK (target_type IN ('CONTAINER','STACK'));

DROP INDEX idx_rules_container;
CREATE INDEX idx_rules_target ON container_rules(target_ref);
