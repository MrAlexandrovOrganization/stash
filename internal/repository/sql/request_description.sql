UPDATE items SET ai_description=NULL,ai_description_error=NULL,updated_at=NOW() WHERE id=$1;
