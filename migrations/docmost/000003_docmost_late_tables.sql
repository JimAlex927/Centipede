-- Compatibility follow-up for installations that already recorded 000002.
-- Keep this migration additive and idempotent; it is intentionally a small
-- delta for the tables introduced by later Docmost and enterprise features.

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
