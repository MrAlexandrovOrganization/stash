"""Exercise preservation/uniqueness and logical restore in an isolated PostgreSQL.

Uses the pinned project image, no host volumes, no network, and only synthetic data.
The fixture models pre-existing heap duplicates by removing the old unique index;
it never corrupts a PostgreSQL page or connects to a configured application DB.
"""

import os
from pathlib import Path
import subprocess
import time
import uuid


ROOT = Path(__file__).resolve().parents[1]
NAME = "stash-file-id-check-" + uuid.uuid4().hex


def run(*args, data=None):
    result = subprocess.run(
        args, input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120
    )
    if result.returncode:
        raise RuntimeError(result.stderr.decode())
    return result.stdout


def sql(statement, database="postgres"):
    return run(
        "docker", "exec", "-i", NAME, "psql", "-X", "-At", "-U", "postgres",
        "-d", database, "-v", "ON_ERROR_STOP=1", data=statement.encode(),
    ).decode().strip()


def check():
    image = "pgvector/pgvector:" + os.environ["PGVECTOR_VERSION"]
    run(
        "docker", "run", "--detach", "--rm", "--network", "none", "--name", NAME,
        "--env", "POSTGRES_HOST_AUTH_METHOD=trust", image,
    )
    try:
        for _ in range(60):
            ready = subprocess.run(
                ["docker", "exec", NAME, "pg_isready", "-U", "postgres"],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10,
            )
            if ready.returncode == 0:
                break
            time.sleep(0.5)
        else:
            raise RuntimeError("fixture PostgreSQL did not become ready")

        for migration in sorted((ROOT / "migrations").glob("*.sql")):
            if migration.name.startswith("011_"):
                break
            sql(migration.read_text())

        sql("""
            DROP INDEX items_file_unique_id_idx;
            CREATE INDEX items_file_unique_id_idx ON items(file_unique_id);
            INSERT INTO items(id,type,file_name,content_type,size,storage_path,
                              file_unique_id,created_at)
            VALUES
              ('old','image','old.jpg','image/jpeg',10,'old/old.jpg','shared','2025-01-01'),
              ('new','image','new.jpg','image/jpeg',20,'new/new.jpg','shared','2025-02-01'),
              ('tie-a','image','a.jpg','image/jpeg',30,'a/a.jpg','tie','2025-03-01'),
              ('tie-b','image','b.jpg','image/jpeg',40,'b/b.jpg','tie','2025-03-01'),
              ('single','image','s.jpg','image/jpeg',50,'s/s.jpg','single','2025-03-01'),
              ('null-a','document','a.txt','text/plain',1,'n/a.txt',NULL,'2025-03-01'),
              ('null-b','document','b.txt','text/plain',2,'n/b.txt',NULL,'2025-03-01');
            UPDATE items SET embedding_text = array_fill(0.5::real, ARRAY[1024])::vector,
                             embedding_image = array_fill(0.5::real, ARRAY[512])::vector;
            CREATE TABLE before_repair AS SELECT id, to_jsonb(items) AS original FROM items;
        """)
        sql("BEGIN;" + (ROOT / "migrations/011_file_unique_id_index_repair.sql").read_text() + "COMMIT;")
        query = (ROOT / "internal/repository/sql/get_by_file_unique_id.sql").read_text()
        # Prepared statements are connection-local; use the actual embedded query
        # in the same session when checking which record receives re-uploads.
        result = sql("PREPARE lookup(text) AS " + query + " EXECUTE lookup('shared');")
        if not any(line.startswith("new|image|") for line in result.splitlines()):
            raise AssertionError("deduplication lookup did not select the newest item")
        sql("""
            DO $$ BEGIN
              IF (SELECT count(*) FROM items) <> 7 OR EXISTS (
                SELECT FROM before_repair b FULL JOIN items i USING(id)
                WHERE b.original IS DISTINCT FROM
                    (to_jsonb(i) - 'file_unique_id_legacy_duplicate')
              ) THEN RAISE EXCEPTION 'original item data changed'; END IF;
              IF (SELECT array_agg(id ORDER BY id) FROM items
                  WHERE file_unique_id_legacy_duplicate) <> ARRAY['old','tie-a']
              THEN RAISE EXCEPTION 'wrong historical copies marked'; END IF;
              BEGIN
                INSERT INTO items(id,type,file_name,content_type,size,storage_path,file_unique_id)
                VALUES ('conflict','image','x.jpg','image/jpeg',1,'x/x.jpg','shared');
                RAISE EXCEPTION 'duplicate owner accepted';
              EXCEPTION WHEN unique_violation THEN NULL;
              END;
            END $$;
            CREATE EXTENSION amcheck;
            SELECT bt_index_check('items_file_unique_id_idx', true);
        """)
        dump = run("docker", "exec", NAME, "pg_dump", "-U", "postgres", "-Fc", "postgres")
        sql("CREATE DATABASE restored")
        run(
            "docker", "exec", "-i", NAME, "pg_restore", "-U", "postgres", "-d", "restored",
            "--exit-on-error", data=dump,
        )
        if sql("SELECT count(*) FROM items", "restored") != "7":
            raise AssertionError("logical restore lost items")
        sql("SELECT bt_index_check('items_file_unique_id_idx', true)", "restored")
        # Ordinary metadata updates must not turn a historical copy into an owner.
        sql("UPDATE items SET description='edited' WHERE id='old'")
        if sql("SELECT file_unique_id_legacy_duplicate FROM items WHERE id='old'") != "t":
            raise AssertionError("editing a historical copy changed its role")
        sql("""
            DELETE FROM items WHERE id='new';
            INSERT INTO items(id,type,file_name,content_type,size,storage_path,file_unique_id)
            VALUES ('replacement','image','r.jpg','image/jpeg',1,'r/r.jpg','shared');
        """)
        if sql("SELECT count(*) FROM items WHERE file_unique_id='shared'") != "2":
            raise AssertionError("replacing an owner lost the historical copy")
        print("file_unique_id migration: preservation, lookup, uniqueness, amcheck and restore passed")
    finally:
        subprocess.run(
            ["docker", "rm", "--force", "--volumes", NAME], check=False,
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30,
        )


if __name__ == "__main__":
    check()
