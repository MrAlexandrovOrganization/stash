WITH source AS (
    SELECT items.*,
        concat_ws(' ', file_name, description, original_caption, transcript,
                  ai_description, array_to_string(tags, ' ')) AS body
    FROM items
), ranked AS (
    SELECT source.*,
        to_tsvector('russian', body) AS ru,
        to_tsvector('english', body) AS en,
        plainto_tsquery('russian', $1) AS ru_query,
        plainto_tsquery('english', $1) AS en_query
    FROM source
), matched AS (
    SELECT ranked.*,
        CASE WHEN $1 = '' THEN 0 ELSE
            ts_rank_cd(ru, ru_query) + ts_rank_cd(en, en_query) +
            CASE WHEN strpos(lower(body), lower($1)) > 0 THEN 1 ELSE 0 END
        END AS relevance
    FROM ranked
    WHERE ($1 = '' OR strpos(lower(body), lower($1)) > 0
           OR ru @@ ru_query OR en @@ en_query)
      AND NOT EXISTS (
          SELECT 1 FROM unnest($2::text[]) AS wanted
          WHERE NOT EXISTS (SELECT 1 FROM unnest(tags) AS tag WHERE lower(tag) = lower(wanted))
      )
      AND NOT EXISTS (
          SELECT 1 FROM unnest(tags) AS tag, unnest($3::text[]) AS excluded
          WHERE lower(tag) = lower(excluded)
      )
)
