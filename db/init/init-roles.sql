-- Provisions the least-privilege database roles used by the application.
-- Runs once, as the postgres superuser, on first container init (via
-- /docker-entrypoint-initdb.d). The application NEVER connects as superuser.
--
-- Two roles, separated by duty (build.md Database Security Rule #1):
--   payments_app        — runtime role: DML only (SELECT/INSERT/UPDATE/DELETE),
--                         no DDL (CREATE/DROP/TRUNCATE). audit_logs is
--                         INSERT-only (no UPDATE/DELETE).
--   payments_migrations — schema role: owns DDL, runs migrations.
--
-- Passwords are injected from the environment by docker-compose; they are not
-- hardcoded here. \gexec runs the dynamically-built statements.

\set app_password `echo "$PAYMENTS_APP_PASSWORD"`
\set migrations_password `echo "$PAYMENTS_MIGRATIONS_PASSWORD"`

-- Create roles (idempotent).
SELECT format('CREATE ROLE payments_app LOGIN PASSWORD %L', :'app_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'payments_app')
\gexec

SELECT format('CREATE ROLE payments_migrations LOGIN PASSWORD %L', :'migrations_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'payments_migrations')
\gexec

-- The migrations role owns the schema and may run DDL.
GRANT ALL ON SCHEMA public TO payments_migrations;

-- The app role may use the schema but not create objects in it.
GRANT USAGE ON SCHEMA public TO payments_app;
REVOKE CREATE ON SCHEMA public FROM payments_app;

-- Default privileges: whenever payments_migrations creates a table, grant the
-- app role DML on it automatically. Tables that need tighter rules (audit_logs)
-- are re-restricted by the migration that creates them.
ALTER DEFAULT PRIVILEGES FOR ROLE payments_migrations IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO payments_app;
ALTER DEFAULT PRIVILEGES FOR ROLE payments_migrations IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO payments_app;
