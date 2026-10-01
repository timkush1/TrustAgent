import { createFileRoute } from '@tanstack/react-router';
import { SetupPage } from '@/components/setup/SetupPage';

export const Route = createFileRoute('/app/setup')({
  staticData: { title: 'Sign in' },
  component: SetupPage,
});
