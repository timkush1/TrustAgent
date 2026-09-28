/**
 * App-wide services created once in main.tsx: runtime config, the DataSource and the query
 * client. Components read them through these hooks; route loaders get the same object as router
 * context.
 */
import type { QueryClient } from '@tanstack/react-query';
import { createContext, useContext } from 'react';
import type { RuntimeConfig } from '@/config/runtime';
import type { DataSource } from '@/data/source';

export interface AppServices {
  config: RuntimeConfig;
  dataSource: DataSource;
  queryClient: QueryClient;
}

export const ServicesContext = createContext<AppServices | null>(null);

export function useServices(): AppServices {
  const services = useContext(ServicesContext);
  if (!services) throw new Error('useServices() needs <ServicesContext.Provider> (see main.tsx)');
  return services;
}

export function useDataSource(): DataSource {
  return useServices().dataSource;
}

export function useRuntimeConfig(): RuntimeConfig {
  return useServices().config;
}
