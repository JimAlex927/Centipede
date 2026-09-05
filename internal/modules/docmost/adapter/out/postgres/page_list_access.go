package postgres

// Applied before ORDER BY/LIMIT so inaccessible rows never occupy list slots.
// Workspace administrators retain space access but still obey page restrictions.
const pageListAccessSQL = `
EXISTS (
 SELECT 1 FROM spaces visible_space
 WHERE visible_space.id = p.space_id AND visible_space.workspace_id = p.workspace_id
 AND visible_space.deleted_at IS NULL
 AND ($9 OR visible_space.visibility = 'public' OR EXISTS (
   SELECT 1 FROM space_members sm
   WHERE sm.space_id = p.space_id AND sm.deleted_at IS NULL
   AND (sm.user_id::text = $8 OR sm.group_id IN (
     SELECT gu.group_id FROM group_users gu WHERE gu.user_id::text = $8
   ))
 ))
)
AND NOT EXISTS (
 WITH RECURSIVE ancestors AS (
   SELECT p.id, p.parent_page_id, ARRAY[p.id] AS visited
   UNION ALL
   SELECT parent.id, parent.parent_page_id, a.visited || parent.id
   FROM pages parent JOIN ancestors a ON parent.id = a.parent_page_id
   WHERE parent.workspace_id = p.workspace_id AND NOT parent.id = ANY(a.visited)
 )
 SELECT 1 FROM ancestors a JOIN page_access pa ON pa.page_id = a.id
 WHERE NOT EXISTS (
   SELECT 1 FROM page_permissions pp WHERE pp.page_access_id = pa.id
   AND (pp.user_id::text = $8 OR pp.group_id IN (
     SELECT gu.group_id FROM group_users gu WHERE gu.user_id::text = $8
   ))
 )
)`
