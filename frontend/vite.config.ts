import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import * as path from "path";

// frontend/ is now directly below the Go repository. Load the repository
// environment so APP_URL and the runtime URL overrides work from this folder.
const envPath = path.resolve(process.cwd(), "..");

export default defineConfig(({ mode }) => {
  const {
    APP_URL,
    API_BASE_URL,
    REALTIME_URL,
    FILE_UPLOAD_SIZE_LIMIT,
    FILE_IMPORT_SIZE_LIMIT,
    DRAWIO_URL,
    CLOUD,
    SUBDOMAIN_HOST,
    COLLAB_URL,
    BILLING_TRIAL_DAYS,
    POSTHOG_HOST,
    POSTHOG_KEY,
    AI_VECTOR_DRIVER,
  } = loadEnv(mode, envPath, "");

  const backendUrl = APP_URL || "http://localhost:7788";

  return {
    define: {
      "process.env": {
        APP_URL,
        API_BASE_URL,
        REALTIME_URL,
        FILE_UPLOAD_SIZE_LIMIT,
        FILE_IMPORT_SIZE_LIMIT,
        DRAWIO_URL,
        CLOUD,
        SUBDOMAIN_HOST,
        COLLAB_URL,
        BILLING_TRIAL_DAYS,
        POSTHOG_HOST,
        POSTHOG_KEY,
        AI_VECTOR_DRIVER,
      },
      APP_VERSION: JSON.stringify(process.env.npm_package_version),
    },
    plugins: [react()],
    build: {
      rolldownOptions: {
        output: {
          advancedChunks: {
            groups: [
              {
                name: "vendor-mantine",
                test: /[\\/]node_modules[\\/]@mantine[\\/]/,
              },
            ],
          },
        },
      },
    },
    resolve: {
      alias: {
        "@": "/src",
      },
    },
    server: {
      proxy: {
        "/api": {
          target: backendUrl,
          changeOrigin: false,
        },
        "/collab": {
          target: backendUrl,
          ws: true,
          rewriteWsOrigin: true,
        },
        "/realtime": {
          target: backendUrl,
          ws: true,
          rewriteWsOrigin: true,
        },
      },
    },
    // `vite preview` does not inherit the dev server proxy automatically.
    // Keeping the same proxy here makes a production build testable without
    // silently sending attachment/API requests to the static frontend port.
    preview: {
      proxy: {
        "/api": {
          target: backendUrl,
          changeOrigin: false,
        },
        "/collab": {
          target: backendUrl,
          ws: true,
          rewriteWsOrigin: true,
        },
        "/realtime": {
          target: backendUrl,
          ws: true,
          rewriteWsOrigin: true,
        },
      },
    },
  };
});
