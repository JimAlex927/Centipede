import { SSO_PROVIDER } from "@/ee/security/contants.ts";
import { getBackendOrigin } from "@/lib/config.ts";

export function buildCallbackUrl(opts: {
  providerId: string;
  type: SSO_PROVIDER;
}): string {
  const { providerId, type } = opts;
  const domain = getBackendOrigin();

  if (type === SSO_PROVIDER.GOOGLE) {
    return `${domain}/api/sso/${type}/callback`;
  }
  return `${domain}/api/sso/${type}/${providerId}/callback`;
}

export function buildSsoLoginUrl(opts: {
  providerId: string;
  type: SSO_PROVIDER;
  workspaceId?: string;
  redirect?: string;
}): string {
  const { providerId, type, workspaceId, redirect } = opts;
  const domain = getBackendOrigin();

  const params = new URLSearchParams();
  if (redirect) params.set("redirect", redirect);

  if (type === SSO_PROVIDER.GOOGLE) {
    if (workspaceId) params.set("workspaceId", workspaceId);
    return `${domain}/api/sso/${type}/login?${params.toString()}`;
  }
  const query = params.toString();
  const base = `${domain}/api/sso/${type}/${providerId}/login`;
  return query ? `${base}?${query}` : base;
}

export function getGoogleSignupUrl(): string {
  // Google login is instance-wide. Use the env APP_URL instead
  return `${getBackendOrigin()}/api/sso/${SSO_PROVIDER.GOOGLE}/signup`;
}

export function buildSamlEntityId(providerId: string): string {
  const domain = getBackendOrigin();
  return `${domain}/api/sso/${SSO_PROVIDER.SAML}/${providerId}/login`;
}
