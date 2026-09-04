import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createRootRoute, Outlet } from "@tanstack/react-router";

import { useSessionRenewal } from "@/api/session";
import { Toaster } from "@/components/ui/sonner";
import { ThemeProvider } from "@/lib/theme";

// One client for the app's lifetime. Retries are off because every failure
// here is a decision the operator should see rather than one the page should
// paper over — a queue that silently retried a rejected approval would be
// worse than one that said it failed.
const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: false, refetchOnWindowFocus: false },
    mutations: { retry: false },
  },
});

export const Route = createRootRoute({
  component: RootLayout,
});

function RootLayout() {
  return (
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <SessionRenewal />
        <Outlet />
        <Toaster position="bottom-right" />
      </ThemeProvider>
    </QueryClientProvider>
  );
}

// Inside the provider, because registering the renewer needs /auth/config and
// that is a query. Renders nothing — it exists for its effect.
function SessionRenewal() {
  useSessionRenewal();
  return null;
}
