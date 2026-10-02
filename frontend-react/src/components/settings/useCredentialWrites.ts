/**
 * The credential writes (#55 PUT, #57 verify, #56 DELETE) with what they invalidate: the
 * credentials, the settings (bindings, D76) and the status. The live stream sends the same changes
 * (credentials_changed, settings_updated), but this tab never waits for it.
 */
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useDataSource } from '@/app/services';
import { qk } from '@/data/queries';
import { qkcSecurity } from '@/data/queries/security';
import type { CredentialPutRequest, CredentialVerifyRequest } from '@/types/api';

export function useInvalidateCredentials() {
  const qc = useQueryClient();
  return () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: qkcSecurity.credentials }),
      qc.invalidateQueries({ queryKey: qkcSecurity.settings }),
      qc.invalidateQueries({ queryKey: qk.status }),
    ]);
}

export function usePutCredential() {
  const ds = useDataSource();
  const invalidate = useInvalidateCredentials();
  return useMutation({
    mutationFn: ({ name, request }: { name: string; request: CredentialPutRequest }) => ds.putCredential(name, request),
    onSettled: invalidate,
  });
}

export function useVerifyCredential() {
  const ds = useDataSource();
  const invalidate = useInvalidateCredentials();
  return useMutation({
    mutationFn: ({ name, request }: { name: string; request?: CredentialVerifyRequest }) => ds.verifyCredential(name, request),
    onSettled: invalidate,
  });
}
