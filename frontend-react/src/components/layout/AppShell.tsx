import { Outlet } from '@tanstack/react-router';
import { NavRail } from './NavRail';
import { TopBar } from './TopBar';

export function AppShell() {
  return (
    <div className="grid min-h-dvh grid-cols-[auto_minmax(0,1fr)] bg-bg text-fg">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50 focus:rounded-md focus:bg-surface-raised focus:px-3 focus:py-2 focus:text-sm"
      >
        Skip to content
      </a>
      <NavRail />
      <div className="flex min-w-0 flex-col">
        <TopBar />
        <main id="main" tabIndex={-1} className="flex-1 px-6 py-5 outline-none">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
