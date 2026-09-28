import { createRootRouteWithContext, Outlet } from '@tanstack/react-router';
import type { AppServices } from '@/app/services';
import { NotFound } from '@/components/common/RouteStates';
import { Toaster } from '@/components/ui/toast';
import { TooltipProvider } from '@/components/ui/tooltip';

function RootLayout() {
  return (
    <TooltipProvider delay={400}>
      <Outlet />
      {/* The themed toaster reads the theme itself (components/ui/toast.tsx). */}
      <Toaster />
    </TooltipProvider>
  );
}

export const Route = createRootRouteWithContext<AppServices>()({
  component: RootLayout,
  notFoundComponent: NotFound,
});
