/**
 * Renders a query through the page contract: nothing for 200 ms, then a skeleton kept for at
 * least 400 ms; the server's message with Retry on error; `empty` when the data has nothing to
 * show; otherwise `children(data)`.
 */
import type { UseQueryResult } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { ErrorState, LoadingState } from './StateViews';
import { useDelayedLoading } from './useDelayedLoading';

export interface QueryStateProps<T> {
  query: Pick<UseQueryResult<T>, 'data' | 'error' | 'isPending' | 'isError' | 'refetch'>;
  children: (data: T) => ReactNode;
  isEmpty?: (data: T) => boolean;
  empty?: ReactNode;
  loading?: ReactNode;
}

export function QueryState<T>({ query, children, isEmpty, empty, loading }: QueryStateProps<T>) {
  const showLoading = useDelayedLoading(query.isPending);
  if (query.isPending || showLoading) return showLoading ? (loading ?? <LoadingState />) : null;
  if (query.isError && query.data === undefined) {
    return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  }
  const data = query.data as T;
  if (isEmpty?.(data) && empty !== undefined) return <>{empty}</>;
  return <>{children(data)}</>;
}
