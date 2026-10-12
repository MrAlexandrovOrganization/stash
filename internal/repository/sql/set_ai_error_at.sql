UPDATE items SET ai_description_error=$2,updated_at=NOW() WHERE id=$1 AND processing_generation=$3;
