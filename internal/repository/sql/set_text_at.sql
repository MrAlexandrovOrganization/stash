UPDATE items SET embedding_text=$2,updated_at=NOW() WHERE id=$1 AND processing_generation=$3;
