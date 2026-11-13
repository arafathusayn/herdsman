-- 0007_execution_experiment.sql (account): the experiment label of the
-- backend that wrote an execution record (docs/experimental.md: anything an
-- experiment writes names its version). A new column only; no existing
-- column changes. herdr-core has only ever written records as the experiment
-- runtime.core/0.1.0, so its existing rows name that label; a herdr-tui row
-- names none.
ALTER TABLE executions ADD COLUMN experiment TEXT NOT NULL DEFAULT '';
UPDATE executions SET experiment = 'runtime.core/0.1.0' WHERE backend = 'herdr-core';
