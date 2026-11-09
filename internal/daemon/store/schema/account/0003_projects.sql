-- Projects, the folders bound to them, their repositories, and their tasks.
-- An archived project keeps its rows and gives up its name.
CREATE TABLE projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL CHECK (state IN ('active','archived')),
  created INTEGER NOT NULL,
  updated INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX projects_active_name ON projects(name) WHERE state = 'active';

-- The primary key is the path: a folder belongs to one project at most.
CREATE TABLE project_folders (
  path TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id)
) STRICT;

CREATE INDEX project_folders_project ON project_folders(project_id);

CREATE TABLE project_repos (
  project_id TEXT NOT NULL REFERENCES projects(id),
  name TEXT NOT NULL,
  path TEXT NOT NULL,
  default_branch TEXT NOT NULL DEFAULT '',
  remote TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (project_id, name)
) STRICT;

CREATE TABLE tasks (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id),
  slug TEXT NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('todo','doing','review','done','archived')),
  route TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL,
  updated INTEGER NOT NULL,
  UNIQUE (project_id, slug),
  UNIQUE (id, project_id)
) STRICT;

-- Enforced here as well as by the absence of an update path in the code.
CREATE TRIGGER tasks_project_fixed BEFORE UPDATE OF project_id ON tasks
WHEN NEW.project_id IS NOT OLD.project_id
BEGIN
  SELECT RAISE(ABORT, 'the project of a task is fixed');
END;

CREATE TABLE task_labels (
  task_id TEXT NOT NULL REFERENCES tasks(id),
  label TEXT NOT NULL,
  PRIMARY KEY (task_id, label)
) STRICT;

-- The two composite keys make a repository of another project unrepresentable.
CREATE TABLE task_repos (
  task_id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  repo TEXT NOT NULL,
  PRIMARY KEY (task_id, repo),
  FOREIGN KEY (task_id, project_id) REFERENCES tasks(id, project_id),
  FOREIGN KEY (project_id, repo) REFERENCES project_repos(project_id, name)
) STRICT;

CREATE INDEX task_repos_repo ON task_repos(project_id, repo);

CREATE TABLE task_links (
  task_id TEXT NOT NULL,
  repo TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('pr','issue','url')),
  target TEXT NOT NULL,
  PRIMARY KEY (task_id, repo, kind, target),
  FOREIGN KEY (task_id, repo) REFERENCES task_repos(task_id, repo)
) STRICT;

-- One row per operation whose record changes have committed, written in the
-- same transaction, so a restart can tell whether they happened.
CREATE TABLE project_commits (
  op_id TEXT PRIMARY KEY,
  created INTEGER NOT NULL
) STRICT;
