import { createFileRoute } from '@tanstack/react-router';
import { PlaygroundPage } from '@/components/playground/PlaygroundPage';

export const Route = createFileRoute('/app/playground')({
  staticData: { title: 'Playground' },
  component: PlaygroundPage,
});
