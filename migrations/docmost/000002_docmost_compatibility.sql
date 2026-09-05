-- Idempotent compatibility upgrade for databases created by older Docmost
-- releases. The baseline remains the preferred path for an empty database.
-- Every operation below is additive; existing rows and values are preserved.

CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
CREATE EXTENSION IF NOT EXISTS unaccent WITH SCHEMA public;

CREATE OR REPLACE FUNCTION public.f_unaccent(text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
    AS $$ SELECT unaccent('unaccent', $1) $$;

-- Columns added after the original Docmost schema.
ALTER TABLE public.workspaces
    ADD COLUMN IF NOT EXISTS enforce_sso boolean DEFAULT false,
    ADD COLUMN IF NOT EXISTS license_key varchar,
    ADD COLUMN IF NOT EXISTS enforce_mfa boolean DEFAULT false,
    ADD COLUMN IF NOT EXISTS is_scim_enabled boolean DEFAULT false,
    ADD COLUMN IF NOT EXISTS stripe_customer_id varchar,
    ADD COLUMN IF NOT EXISTS status varchar,
    ADD COLUMN IF NOT EXISTS plan varchar,
    ADD COLUMN IF NOT EXISTS billing_email varchar,
    ADD COLUMN IF NOT EXISTS trial_end_at timestamptz;

ALTER TABLE public.pages
    ADD COLUMN IF NOT EXISTS contributor_ids uuid[] DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS is_base boolean DEFAULT false,
    ADD COLUMN IF NOT EXISTS base_schema_version integer DEFAULT 1;

ALTER TABLE public.page_history
    ADD COLUMN IF NOT EXISTS contributor_ids uuid[] DEFAULT '{}';

ALTER TABLE public.spaces
    ADD COLUMN IF NOT EXISTS settings jsonb DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS is_personal boolean DEFAULT false;

ALTER TABLE public.attachments
    ADD COLUMN IF NOT EXISTS text_content text,
    ADD COLUMN IF NOT EXISTS tsv tsvector,
    ADD COLUMN IF NOT EXISTS ai_chat_id uuid;

ALTER TABLE public.comments
    ADD COLUMN IF NOT EXISTS last_edited_by_id uuid,
    ADD COLUMN IF NOT EXISTS resolved_by_id uuid,
    ADD COLUMN IF NOT EXISTS updated_at timestamptz DEFAULT now(),
    ADD COLUMN IF NOT EXISTS space_id uuid;

ALTER TABLE IF EXISTS public.notifications
    ADD COLUMN IF NOT EXISTS page_verification_id uuid;

ALTER TABLE IF EXISTS public.file_tasks
    ADD COLUMN IF NOT EXISTS page_id uuid,
    ADD COLUMN IF NOT EXISTS metadata jsonb;

ALTER TABLE public.users
    ADD COLUMN IF NOT EXISTS scim_external_id text;

ALTER TABLE public.groups
    ADD COLUMN IF NOT EXISTS scim_external_id text,
    ADD COLUMN IF NOT EXISTS is_external boolean DEFAULT false;

-- Newer feature tables. They intentionally use nullable columns when added to
-- an existing table: old rows must remain valid during a rolling upgrade.
CREATE TABLE IF NOT EXISTS public.ai_chats (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    creator_id uuid NOT NULL,
    title varchar,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz
);

CREATE TABLE IF NOT EXISTS public.ai_chat_messages (
    id uuid PRIMARY KEY,
    chat_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    user_id uuid,
    role varchar NOT NULL,
    content text,
    tool_calls jsonb,
    metadata jsonb,
    tsv tsvector,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz
);

CREATE TABLE IF NOT EXISTS public.templates (
    id uuid PRIMARY KEY,
    title varchar,
    description text,
    content jsonb,
    ydoc bytea,
    icon varchar,
    space_id uuid,
    workspace_id uuid NOT NULL,
    creator_id uuid,
    last_updated_by_id uuid,
    collaborator_ids uuid[],
    text_content text,
    tsv tsvector,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz
);

ALTER TABLE IF EXISTS public.templates
    ADD COLUMN IF NOT EXISTS title varchar,
    ADD COLUMN IF NOT EXISTS content jsonb,
    ADD COLUMN IF NOT EXISTS ydoc bytea,
    ADD COLUMN IF NOT EXISTS icon varchar,
    ADD COLUMN IF NOT EXISTS space_id uuid,
    ADD COLUMN IF NOT EXISTS workspace_id uuid,
    ADD COLUMN IF NOT EXISTS creator_id uuid,
    ADD COLUMN IF NOT EXISTS last_updated_by_id uuid,
    ADD COLUMN IF NOT EXISTS collaborator_ids uuid[],
    ADD COLUMN IF NOT EXISTS text_content text,
    ADD COLUMN IF NOT EXISTS tsv tsvector;

CREATE TABLE IF NOT EXISTS public.favorites (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL,
    page_id uuid,
    space_id uuid,
    template_id uuid,
    type varchar NOT NULL,
    workspace_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS public.page_verifications (
    id uuid PRIMARY KEY,
    page_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    space_id uuid NOT NULL,
    type varchar DEFAULT 'expiring' NOT NULL,
    status varchar,
    mode varchar,
    period_amount integer,
    period_unit varchar,
    verified_at timestamptz,
    verified_by_id uuid,
    expires_at timestamptz,
    requested_at timestamptz,
    requested_by_id uuid,
    rejected_at timestamptz,
    rejected_by_id uuid,
    rejection_comment text,
    data jsonb,
    creator_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS public.page_verifiers (
    id uuid PRIMARY KEY,
    page_verification_id uuid NOT NULL,
    user_id uuid NOT NULL,
    is_primary boolean DEFAULT false NOT NULL,
    added_by_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS public.scim_tokens (
    id uuid PRIMARY KEY,
    name varchar NOT NULL,
    token_hash varchar NOT NULL,
    token_last_four varchar(4) NOT NULL,
    last_used_at timestamptz,
    is_enabled boolean DEFAULT true NOT NULL,
    creator_id uuid,
    workspace_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz
);

CREATE TABLE IF NOT EXISTS public.page_transclusions (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    page_id uuid NOT NULL,
    transclusion_id varchar NOT NULL,
    content jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS public.page_transclusion_references (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    reference_page_id uuid NOT NULL,
    source_page_id uuid NOT NULL,
    transclusion_id varchar NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS public.labels (
    id uuid PRIMARY KEY,
    name varchar NOT NULL,
    type varchar DEFAULT 'page' NOT NULL,
    workspace_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS public.page_labels (
    id uuid PRIMARY KEY,
    page_id uuid NOT NULL,
    label_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS public.base_properties (
    id varchar PRIMARY KEY,
    page_id uuid NOT NULL,
    name varchar NOT NULL,
    type varchar NOT NULL,
    position varchar NOT NULL,
    type_options jsonb,
    pending_type varchar,
    pending_type_options jsonb,
    pending_token uuid,
    is_primary boolean DEFAULT false NOT NULL,
    schema_version integer DEFAULT 1 NOT NULL,
    workspace_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz
);

CREATE TABLE IF NOT EXISTS public.base_rows (
    id uuid PRIMARY KEY,
    page_id uuid NOT NULL,
    cells jsonb DEFAULT '{}' NOT NULL,
    position varchar NOT NULL,
    creator_id uuid,
    last_updated_by_id uuid,
    workspace_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz
);

CREATE TABLE IF NOT EXISTS public.base_views (
    id uuid PRIMARY KEY,
    page_id uuid NOT NULL,
    name varchar NOT NULL,
    type varchar DEFAULT 'table' NOT NULL,
    position varchar NOT NULL,
    config jsonb DEFAULT '{}' NOT NULL,
    workspace_id uuid NOT NULL,
    creator_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS public.user_tokens (
    id uuid PRIMARY KEY,
    token varchar NOT NULL,
    type varchar NOT NULL,
    user_id uuid NOT NULL,
    workspace_id uuid,
    expires_at timestamptz,
    used_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS public.backlinks (
    id uuid PRIMARY KEY,
    source_page_id uuid NOT NULL,
    target_page_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS public.shares (
    id uuid PRIMARY KEY,
    key varchar NOT NULL,
    page_id uuid,
    include_sub_pages boolean DEFAULT false,
    search_indexing boolean DEFAULT false,
    creator_id uuid,
    space_id uuid,
    workspace_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz
);

CREATE TABLE IF NOT EXISTS public.billing (
    id uuid PRIMARY KEY,
    stripe_subscription_id varchar,
    stripe_customer_id varchar,
    status varchar,
    quantity bigint,
    amount bigint,
    interval varchar,
    currency varchar,
    metadata jsonb,
    stripe_price_id varchar,
    stripe_item_id varchar,
    stripe_product_id varchar,
    period_start_at timestamptz,
    period_end_at timestamptz,
    cancel_at_period_end boolean,
    cancel_at timestamptz,
    canceled_at timestamptz,
    ended_at timestamptz,
    workspace_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz,
    billing_scheme varchar,
    tiered_up_to varchar,
    tiered_flat_amount bigint,
    tiered_unit_amount bigint,
    plan_name varchar
);

CREATE TABLE IF NOT EXISTS public.auth_accounts (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL,
    provider_user_id varchar NOT NULL,
    auth_provider_id uuid,
    workspace_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz
);

CREATE TABLE IF NOT EXISTS public.auth_providers (
    id uuid PRIMARY KEY,
    name varchar NOT NULL,
    type text NOT NULL,
    saml_url varchar,
    saml_certificate varchar,
    oidc_issuer varchar,
    oidc_client_id varchar,
    oidc_client_secret varchar,
    allow_signup boolean DEFAULT false NOT NULL,
    is_enabled boolean DEFAULT false NOT NULL,
    creator_id uuid,
    workspace_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz,
    group_sync boolean DEFAULT false NOT NULL,
    ldap_url varchar,
    ldap_bind_dn varchar,
    ldap_bind_password varchar,
    ldap_base_dn varchar,
    ldap_user_search_filter varchar,
    ldap_user_attributes jsonb DEFAULT '{}',
    ldap_tls_enabled boolean DEFAULT false,
    ldap_tls_ca_cert text,
    ldap_config jsonb DEFAULT '{}',
    settings jsonb DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS public.notifications (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    type text NOT NULL,
    actor_id uuid,
    page_id uuid,
    space_id uuid,
    comment_id uuid,
    data jsonb,
    read_at timestamptz,
    emailed_at timestamptz,
    archived_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    page_verification_id uuid
);

CREATE TABLE IF NOT EXISTS public.watchers (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL,
    page_id uuid,
    space_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    type text NOT NULL,
    added_by_id uuid,
    muted_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS public.page_access (
    id uuid PRIMARY KEY,
    page_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    space_id uuid NOT NULL,
    access_level varchar NOT NULL,
    creator_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS public.page_permissions (
    id uuid PRIMARY KEY,
    page_access_id uuid NOT NULL,
    user_id uuid,
    group_id uuid,
    role varchar NOT NULL,
    added_by_id uuid,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS public.audit (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    actor_id uuid,
    actor_type varchar DEFAULT 'user' NOT NULL,
    event varchar NOT NULL,
    resource_type varchar NOT NULL,
    resource_id uuid,
    space_id uuid,
    changes jsonb,
    metadata jsonb,
    ip_address inet,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS public.user_sessions (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    device_name varchar,
    user_agent text,
    ip_address inet,
    geo_location varchar,
    last_active_at timestamptz DEFAULT now() NOT NULL,
    expires_at timestamptz NOT NULL,
    metadata jsonb,
    revoked_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS public.api_keys (
    id uuid PRIMARY KEY,
    name text,
    creator_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    expires_at timestamptz,
    last_used_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz
);

CREATE TABLE IF NOT EXISTS public.oauth_clients (
    id uuid PRIMARY KEY,
    name text NOT NULL,
    redirect_uris jsonb NOT NULL,
    client_uri text,
    logo_uri text,
    grant_types jsonb NOT NULL,
    scopes jsonb NOT NULL,
    token_endpoint_auth_method text DEFAULT 'none' NOT NULL,
    secret_hash text,
    is_dynamic boolean DEFAULT true NOT NULL,
    workspace_id uuid NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    deleted_at timestamptz
);

CREATE TABLE IF NOT EXISTS public.oauth_authorization_codes (
    id uuid PRIMARY KEY,
    code_hash text NOT NULL,
    client_id uuid NOT NULL,
    user_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    scopes jsonb NOT NULL,
    redirect_uri text NOT NULL,
    code_challenge text,
    code_challenge_method text,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS public.oauth_grants (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL,
    client_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    scopes jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    last_used_at timestamptz,
    revoked_at timestamptz
);

CREATE TABLE IF NOT EXISTS public.oauth_tokens (
    id uuid PRIMARY KEY,
    grant_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    access_token_jti text NOT NULL,
    refresh_token_hash text,
    scopes jsonb NOT NULL,
    access_expires_at timestamptz NOT NULL,
    refresh_expires_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz DEFAULT now() NOT NULL
);

-- Bring partially upgraded tables up to the columns used by the Go handlers.
ALTER TABLE IF EXISTS public.auth_providers
    ADD COLUMN IF NOT EXISTS group_sync boolean DEFAULT false,
    ADD COLUMN IF NOT EXISTS ldap_url varchar,
    ADD COLUMN IF NOT EXISTS ldap_bind_dn varchar,
    ADD COLUMN IF NOT EXISTS ldap_bind_password varchar,
    ADD COLUMN IF NOT EXISTS ldap_base_dn varchar,
    ADD COLUMN IF NOT EXISTS ldap_user_search_filter varchar,
    ADD COLUMN IF NOT EXISTS ldap_user_attributes jsonb DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS ldap_tls_enabled boolean DEFAULT false,
    ADD COLUMN IF NOT EXISTS ldap_tls_ca_cert text,
    ADD COLUMN IF NOT EXISTS ldap_config jsonb DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS settings jsonb DEFAULT '{}';

ALTER TABLE IF EXISTS public.billing
    ADD COLUMN IF NOT EXISTS billing_scheme varchar,
    ADD COLUMN IF NOT EXISTS tiered_up_to varchar,
    ADD COLUMN IF NOT EXISTS tiered_flat_amount bigint,
    ADD COLUMN IF NOT EXISTS tiered_unit_amount bigint,
    ADD COLUMN IF NOT EXISTS plan_name varchar;

-- Additive indexes used by search, imports and the enterprise modules.
CREATE INDEX IF NOT EXISTS attachments_tsv_idx ON public.attachments USING gin (tsv);
CREATE INDEX IF NOT EXISTS idx_attachments_ai_chat_id ON public.attachments (ai_chat_id);
CREATE INDEX IF NOT EXISTS idx_pages_is_base ON public.pages (is_base);
CREATE INDEX IF NOT EXISTS idx_file_tasks_page_export ON public.file_tasks (page_id);
CREATE INDEX IF NOT EXISTS idx_users_workspace_scim_external_id ON public.users (workspace_id, scim_external_id);
CREATE INDEX IF NOT EXISTS idx_groups_workspace_scim_external_id ON public.groups (workspace_id, scim_external_id);
CREATE INDEX IF NOT EXISTS pages_title_trgm_idx ON public.pages USING gin (lower(title) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS attachments_file_name_trgm_idx ON public.attachments USING gin (lower(translate(file_name, '_.-', '   ')) gin_trgm_ops);

-- Keep search vectors current when an older installation did not yet have the
-- corresponding trigger. The trigger functions are additive and replace only
-- the trigger definition, never application data.
CREATE OR REPLACE FUNCTION public.templates_tsvector_trigger() RETURNS trigger
    LANGUAGE plpgsql AS $$
    BEGIN
      NEW.tsv :=
        setweight(to_tsvector('english', f_unaccent(coalesce(NEW.title, ''))), 'A') ||
        setweight(to_tsvector('english', f_unaccent(substring(coalesce(NEW.text_content, ''), 1, 1000000))), 'B');
      RETURN NEW;
    END;
    $$;

DO $$
BEGIN
  IF to_regclass('public.templates') IS NOT NULL THEN
    DROP TRIGGER IF EXISTS templates_tsvector_update ON public.templates;
    CREATE TRIGGER templates_tsvector_update
      BEFORE INSERT OR UPDATE ON public.templates
      FOR EACH ROW EXECUTE FUNCTION public.templates_tsvector_trigger();
  END IF;
END $$;
