WITH distances AS (
    SELECT items.*,
        CASE WHEN $1::vector IS NOT NULL THEN 1 - (embedding_text <=> $1::vector) END AS text_score,
        CASE WHEN $2::vector IS NOT NULL THEN 1 - (embedding_image <=> $2::vector) END AS image_score
    FROM items
    WHERE NOT EXISTS (
        SELECT 1 FROM unnest($7::text[]) AS wanted
        WHERE NOT EXISTS (SELECT 1 FROM unnest(tags) AS tag WHERE lower(tag) = lower(wanted))
    )
    AND NOT EXISTS (
        SELECT 1 FROM unnest(tags) AS tag, unnest($8::text[]) AS excluded
        WHERE lower(tag) = lower(excluded)
    )
), scored AS (
    SELECT distances.*,
        (coalesce(text_score * $3::double precision, 0) + coalesce(image_score * $4::double precision, 0)) /
        nullif(CASE WHEN text_score IS NOT NULL THEN $3::double precision ELSE 0 END +
               CASE WHEN image_score IS NOT NULL THEN $4::double precision ELSE 0 END, 0) AS score
    FROM distances
)
SELECT %s FROM scored
WHERE score IS NOT NULL AND score > 0
ORDER BY score DESC NULLS LAST, created_at DESC, id DESC
LIMIT $5 OFFSET $6
