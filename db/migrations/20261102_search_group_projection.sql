-- Explicit, resumable projection migration. Run through tools/backfill_search_groups.py.
-- Each page locks at most batch_limit existing queue rows. Active worker leases
-- stay intact; completion of an older revision leaves the new revision pending.
WITH candidates AS (
  SELECT kind,source_id FROM search_sync_state
  WHERE (kind,source_id) > (:'after_kind',:'after_source_id')
  ORDER BY kind,source_id LIMIT :batch_limit
  FOR UPDATE
), queued AS (
  UPDATE search_sync_state q
  SET desired_revision=GREATEST(q.desired_revision,q.indexed_revision)+1,
      available_at=CASE WHEN q.desired_revision>q.indexed_revision
        THEN LEAST(q.available_at,NOW()) ELSE NOW() END
  FROM candidates c WHERE q.kind=c.kind AND q.source_id=c.source_id
  RETURNING q.kind,q.source_id
), last_row AS (
  SELECT kind,source_id FROM queued ORDER BY kind DESC,source_id DESC LIMIT 1
)
SELECT json_build_object('queued',(SELECT COUNT(*) FROM queued),
  'after_kind',COALESCE((SELECT kind FROM last_row),:'after_kind'),
  'after_source_id',COALESCE((SELECT source_id FROM last_row),:'after_source_id'));
