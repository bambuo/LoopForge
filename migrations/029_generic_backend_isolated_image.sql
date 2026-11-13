-- Align the seeded generic-backend Dockerfile with runtime.IsolatedDockerfile:
-- the shared isolated image installs openssh-client for Git SSH, and an exact
-- match lets generic-docker sandboxes reuse the installed runtime image (with
-- its mounted on-demand CLI directory) instead of building a separate image.
-- Manual migration: run once on installations created before this change.
UPDATE dockerfiles
SET payload = REPLACE(payload, 'git python3 ripgrep', 'git openssh-client python3 ripgrep')
WHERE id = 'generic-backend'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.content')) LIKE '%--no-install-recommends git python3 ripgrep%';
