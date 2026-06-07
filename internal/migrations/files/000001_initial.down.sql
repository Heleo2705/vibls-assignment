DROP TRIGGER IF EXISTS set_jobs_updated_at ON jobs;
DROP FUNCTION IF EXISTS update_updated_at_column();
DROP TABLE IF EXISTS job_events;
DROP TABLE IF EXISTS jobs;
