-- admin_audit_log is append-only (D44). 0005 refuses UPDATE and DELETE row by row, but TRUNCATE
-- fires no row trigger, so a statement trigger refuses it too. Re-running this file is a no-op.
DROP TRIGGER IF EXISTS admin_audit_log_no_truncate ON admin_audit_log;
CREATE TRIGGER admin_audit_log_no_truncate BEFORE TRUNCATE ON admin_audit_log
  FOR EACH STATEMENT EXECUTE FUNCTION trustagent_forbid_audit_change();
