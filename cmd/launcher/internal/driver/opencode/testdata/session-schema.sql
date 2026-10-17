-- `project` and `session` tables, last regenerated from opencode 1.18.35
-- (pkgs.opencode in the flake's locked nixpkgs), verbatim from sqlite_master,
-- each with a `;` appended.
-- The nix check `opencode-session-schema` fails when pkgs.opencode's
-- project/session columns drift from this file. To regenerate, run this from
-- the repo root, replace the statements below this header with its output, and
-- update the version above:
--
--   nix shell --inputs-from . nixpkgs#opencode nixpkgs#sqlite -c sh -c 'd=$(mktemp -d) && HOME=$d OPENCODE_DB=$d/opencode.db opencode session list >/dev/null && sqlite3 $d/opencode.db "SELECT sql || '"'"';'"'"' FROM sqlite_master WHERE name IN ('"'"'project'"'"', '"'"'session'"'"') ORDER BY name"'
--
CREATE TABLE `project` (
          `id` text PRIMARY KEY,
          `worktree` text NOT NULL,
          `vcs` text,
          `name` text,
          `icon_url` text,
          `icon_url_override` text,
          `icon_color` text,
          `time_created` integer NOT NULL,
          `time_updated` integer NOT NULL,
          `time_initialized` integer,
          `sandboxes` text NOT NULL,
          `commands` text
        );
CREATE TABLE `session` (
          `id` text PRIMARY KEY,
          `project_id` text NOT NULL,
          `workspace_id` text,
          `parent_id` text,
          `slug` text NOT NULL,
          `directory` text NOT NULL,
          `path` text,
          `title` text NOT NULL,
          `version` text NOT NULL,
          `share_url` text,
          `summary_additions` integer,
          `summary_deletions` integer,
          `summary_files` integer,
          `summary_diffs` text,
          `metadata` text,
          `cost` real DEFAULT 0 NOT NULL,
          `tokens_input` integer DEFAULT 0 NOT NULL,
          `tokens_output` integer DEFAULT 0 NOT NULL,
          `tokens_reasoning` integer DEFAULT 0 NOT NULL,
          `tokens_cache_read` integer DEFAULT 0 NOT NULL,
          `tokens_cache_write` integer DEFAULT 0 NOT NULL,
          `revert` text,
          `permission` text,
          `agent` text,
          `model` text,
          `time_created` integer NOT NULL,
          `time_updated` integer NOT NULL,
          `time_compacting` integer,
          `time_archived` integer,
          CONSTRAINT `fk_session_project_id_project_id_fk` FOREIGN KEY (`project_id`) REFERENCES `project`(`id`) ON DELETE CASCADE
        );
