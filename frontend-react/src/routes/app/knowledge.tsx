import { createFileRoute } from '@tanstack/react-router';
import { KnowledgePage } from '@/components/knowledge/KnowledgePage';

export const Route = createFileRoute('/app/knowledge')({
  staticData: { title: 'Knowledge' },
  component: KnowledgePage,
});
