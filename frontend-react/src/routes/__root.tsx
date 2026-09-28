import { createRootRouteWithContext, Outlet } from '@tanstack/react-router';
import { Toaster } from 'sonner';
import type { AppServices } from '@/app/services';
import { NotFound } from '@/components/common/RouteStates';
import { TooltipProvider } from '@/components/ui/tooltip';
import { useThemePreference } from '@/lib/theme';

function RootLayout() {
  const theme = useThemePreference();
  return (
    <TooltipProvider delay={400}>
      <Outlet />
      <Toaster theme={theme} position="bottom-right" closeButton richColors={false} />
    </TooltipProvider>
  );
}

export const Route = createRootRouteWithContext<AppServices>()({
  component: RootLayout,
  notFoundComponent: NotFound,
});
