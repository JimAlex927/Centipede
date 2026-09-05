import { useAtom, useSetAtom } from "jotai";
import { currentUserAtom } from "@/features/user/atoms/current-user-atom";
import React, { useEffect } from "react";
import useCurrentUser from "@/features/user/hooks/use-current-user";
import { useTranslation } from "react-i18next";
import { socketAtom } from "@/features/websocket/atoms/socket-atom.ts";
import { SOCKET_URL } from "@/features/websocket/types";
import { RealtimeClient } from "@/features/websocket/realtime-client";
import { useQuerySubscription } from "@/features/websocket/use-query-subscription.ts";
import { useTreeSocket } from "@/features/websocket/use-tree-socket.ts";
import { useNotificationSocket } from "@/features/notification/hooks/use-notification-socket.ts";
import { useCollabToken } from "@/features/auth/queries/auth-query.tsx";
import { Error404 } from "@/components/ui/error-404.tsx";
import { useEntitlements } from "@/ee/entitlement/use-entitlements";
import { entitlementAtom } from "@/ee/entitlement/entitlement-atom";
import { useQueryClient } from "@tanstack/react-query";

export function UserProvider({ children }: React.PropsWithChildren) {
  const [, setCurrentUser] = useAtom(currentUserAtom);
  const setEntitlements = useSetAtom(entitlementAtom);
  const { data, isLoading, error, isError } = useCurrentUser();
  const { data: entitlements } = useEntitlements();
  const { i18n } = useTranslation();
  const setSocket = useSetAtom(socketAtom);
  const queryClient = useQueryClient();
  // fetch collab token on load
  const { data: collab } = useCollabToken();

  useEffect(() => {
    if (isLoading || isError || !data?.user?.id || !data?.workspace?.id) {
      return;
    }

    const newSocket = new RealtimeClient(SOCKET_URL);
    setSocket(newSocket);
    let hasConnected = false;
    newSocket.on("connect", () => {
      // Re-fetch after an outage: messages sent while offline are not replayed
      // by the Go event hub. This also refreshes active tree/comment queries.
      if (hasConnected) void queryClient.invalidateQueries();
      hasConnected = true;
    });
    newSocket.connect();

    return () => {
      newSocket.disconnect();
      setSocket((current) => current === newSocket ? null : current);
    };
  }, [isError, isLoading, data?.user?.id, data?.workspace?.id, queryClient, setSocket]);

  useQuerySubscription();
  useTreeSocket();
  useNotificationSocket();

  useEffect(() => {
    if (data && data.user && data.workspace) {
      setCurrentUser(data);
      i18n.changeLanguage(
        data.user.locale === "en" ? "en-US" : data.user.locale,
      );
    }
  }, [data, isLoading]);

  useEffect(() => {
    document.documentElement.lang = i18n.resolvedLanguage || i18n.language || "en-US";
  }, [i18n.language, i18n.resolvedLanguage]);

  useEffect(() => {
    if (entitlements) {
      setEntitlements(entitlements);
    }
  }, [entitlements]);

  if (isLoading) return <></>;

  if (isError && error?.["response"]?.status === 404) {
    return <Error404 />;
  }

  if (error) {
    return <></>;
  }

  return <>{children}</>;
}
