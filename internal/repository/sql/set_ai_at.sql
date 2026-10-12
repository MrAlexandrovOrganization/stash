UPDATE items SET ai_description=$2,ai_description_error=NULL,updated_at=NOW() WHERE id=$1 AND processing_generation=$3;
