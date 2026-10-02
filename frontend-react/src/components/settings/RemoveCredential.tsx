/**
 * Remove a saved key (#56) behind the danger confirmation. The proxy answers 409
 * `confirmation_required` when the key runs the fast check for routes that fail closed (P4-PLAN
 * D77): the dialog then names those routes and asks again, and "Remove anyway" retries with
 * `?confirm=true`.
 */
import { Trash2 } from 'lucide-react';
import { useState } from 'react';
import { useDataSource } from '@/app/services';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { ConfirmAgain } from '@/components/common/confirmAgain';
import type { Reason } from '@/components/common/DisabledReason';
import { notify } from '@/components/common/notify';
import { Button } from '@/components/ui/button';
import { copy, fillTemplate, secCredentialLabel, secJoinWords, secUsedByLabel } from '@/domain/presentation';
import type { CredentialView } from '@/types/api';
import { confirmationRoutes } from './credentialModel';
import { GroupedAction } from './SettingsKit';
import { useInvalidateCredentials } from './useCredentialWrites';

export function RemoveCredential({ credential, writeReason, reasonId }: { credential: CredentialView; writeReason: Reason | null; reasonId?: string }) {
  const ds = useDataSource();
  const invalidate = useInvalidateCredentials();
  const [routes, setRoutes] = useState<string[] | null>(null);
  const name = secCredentialLabel(credential.name);
  const impact =
    credential.used_by.length > 0
      ? fillTemplate(copy.settings.credential_remove_impact, { name, used_for: secJoinWords(credential.used_by.map(secUsedByLabel)) })
      : 'Nothing uses it right now.';

  return (
    <GroupedAction reason={writeReason} reasonId={reasonId}>
      {(p) => (
        <ConfirmDialog
          trigger={
            <Button size="sm" aria-label={`Remove the ${name} key`} {...p}>
              <Trash2 aria-hidden="true" />
              Remove
            </Button>
          }
          onOpenChange={(open) => {
            if (!open) setRoutes(null);
          }}
          title={`Remove the ${name} key?`}
          description={`${impact} The key is deleted from this TrustAgent; you can paste it again later.`}
          confirmLabel={routes ? 'Remove anyway' : 'Remove key'}
          onConfirm={async () => {
            try {
              await ds.deleteCredential(credential.name, routes ? { confirm: true } : undefined);
            } catch (err) {
              const needed = confirmationRoutes(err);
              if (needed && !routes) {
                setRoutes(needed);
                throw new ConfirmAgain();
              }
              throw err;
            }
            await invalidate();
            notify.success(`Removed the ${name} key`);
          }}
        >
          {routes ? (
            <p className="rounded-md border border-border-strong px-3 py-2 text-sm text-fg">
              {fillTemplate(copy.settings.fail_closed_confirm, { routes: routes.join(', ') })}
            </p>
          ) : null}
        </ConfirmDialog>
      )}
    </GroupedAction>
  );
}
