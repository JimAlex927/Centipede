-- Optional semantic search storage. JSONB avoids requiring the pgvector
-- extension, so self-hosted PostgreSQL installations remain compatible.
CREATE TABLE IF NOT EXISTS public.ai_embedding_jobs (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    page_id uuid,
    attachment_id uuid,
    status varchar NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    next_run_at timestamptz NOT NULL DEFAULT now(),
    error_message text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ai_embedding_jobs_one_source CHECK ((page_id IS NOT NULL) <> (attachment_id IS NOT NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS ai_embedding_jobs_page_unique
    ON public.ai_embedding_jobs (workspace_id, page_id)
    WHERE page_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS ai_embedding_jobs_attachment_unique
    ON public.ai_embedding_jobs (workspace_id, attachment_id)
    WHERE attachment_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS ai_embedding_jobs_pending_idx
    ON public.ai_embedding_jobs (status, next_run_at, created_at);

CREATE TABLE IF NOT EXISTS public.ai_embeddings (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    page_id uuid,
    attachment_id uuid,
    model_name varchar NOT NULL,
    chunk_index integer NOT NULL DEFAULT 0,
    content text NOT NULL,
    embedding jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ai_embeddings_one_source CHECK ((page_id IS NOT NULL) <> (attachment_id IS NOT NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS ai_embeddings_page_chunk_unique
    ON public.ai_embeddings (workspace_id, page_id, model_name, chunk_index)
    WHERE page_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS ai_embeddings_attachment_chunk_unique
    ON public.ai_embeddings (workspace_id, attachment_id, model_name, chunk_index)
    WHERE attachment_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS ai_embeddings_workspace_model_idx
    ON public.ai_embeddings (workspace_id, model_name);
