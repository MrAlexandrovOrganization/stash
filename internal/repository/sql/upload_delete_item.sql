DELETE FROM items WHERE id = $1 RETURNING storage_path;
