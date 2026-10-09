/**
 * The first-run wizard at /app/setup (SPEC §3.3; ON-7, D87): three steps from the proxy's
 * one-time link to a checked answer.
 *
 * 1. Secure this dashboard: claim the link (or paste its token), save the admin key, or sign in.
 * 2. Connect OpenRouter: Connect, paste a key, or try simulated checks. On the demo stack it is
 *    done already: the demo runs simulated checks and has nothing to connect.
 * 3. See it work: the refund example, flagged, and an app key for your own requests.
 *
 * On the demo stack (`make demo` opens this page with its one-time link) the wizard never dead-ends:
 * step 1, then step 3 straight away. The demo banner says how to use your own keys, once.
 *
 * `?step=` opens a step; without it the wizard opens the first step not done, and stays on step 1
 * after a sign-in or claim until the viewer moves on (the admin key must be saved first). It is also
 * the dashboard's sign-in page.
 */
import { useQuery } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { ArrowLeft, ArrowRight, Check } from 'lucide-react';
import { useState } from 'react';
import { useDataSource } from '@/app/services';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { PageHeader } from '@/components/common/PageHeader';
import { LoadingState } from '@/components/common/StateViews';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { useSettingsWriteReason } from '@/components/settings/useSettingsAccess';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardFooter, CardHeader } from '@/components/ui/card';
import { setupStatusQuery } from '@/data/queries';
import { useAppMode } from '@/data/useSystem';
import { copy } from '@/domain/presentation';
import { cn } from '@/lib/cn';
import { takeSetupToken } from './setupToken';
import { StepConnect, StepConnectDemo } from './StepConnect';
import { StepSecure } from './StepSecure';
import { StepSeeItWork } from './StepSeeItWork';
import { useDashboardAuth } from './useDashboardAuth';
import { SETUP_STEPS, useSetupProgress, type SetupStep } from './useSetupProgress';

const TITLES: Record<SetupStep, string> = {
  secure: copy.setup.step_secure.title,
  connect: copy.setup.step_connect.title,
  try: copy.setup.step_see_it_work.title,
};

function Stepper({ current, done, locked }: { current: SetupStep; done: Record<SetupStep, boolean>; locked: boolean }) {
  return (
    <nav aria-label="Setup steps">
      <ol className="grid gap-2 sm:grid-cols-3">
        {SETUP_STEPS.map((step, i) => {
          const isCurrent = step === current;
          const inner = (
            <>
              <span
                aria-hidden="true"
                className={cn(
                  'flex size-6 shrink-0 items-center justify-center rounded-full border text-xs tabular',
                  done[step] ? 'border-tone-success text-tone-success' : isCurrent ? 'border-fg text-fg' : 'border-border-strong',
                )}
              >
                {done[step] ? <Check className="size-3.5" /> : i + 1}
              </span>
              <span className="min-w-0">
                <span className="block font-medium">{TITLES[step]}</span>
                <span className="block text-xs">{done[step] ? 'Done' : isCurrent ? 'Now' : 'Not yet'}</span>
              </span>
            </>
          );
          const box = cn('flex items-center gap-2 rounded-md border px-3 py-2 text-sm', isCurrent ? 'border-fg bg-surface text-fg' : 'border-border text-fg-muted');
          return (
            <li key={step}>
              {/* While a key shown once waits for "I saved it", the steps are progress only: leaving
                  this step would take the key off the screen before it is saved. */}
              {locked ? (
                <span aria-current={isCurrent ? 'step' : undefined} className={box}>
                  {inner}
                </span>
              ) : (
                <Link
                  to="/app/setup"
                  search={{ step }}
                  aria-current={isCurrent ? 'step' : undefined}
                  className={cn(box, isCurrent ? null : 'hover:bg-surface-hover hover:text-fg')}
                >
                  {inner}
                </Link>
              )}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}

export function SetupWizard({ step }: { step?: SetupStep }) {
  const ds = useDataSource();
  const navigate = useNavigate();
  // Read once: the token leaves the address bar on the first render.
  const [token] = useState(takeSetupToken);
  const [saved, setSaved] = useState(false);
  const setup = useQuery(setupStatusQuery(ds));
  const auth = useDashboardAuth();
  const progress = useSetupProgress();
  const mode = useAppMode();
  const writeReason = useSettingsWriteReason();

  // Stay on step 1 after a sign-in or a claim here, so the viewer sees it worked (and saves the key).
  const stay = auth.createdKey !== null || auth.signIn.isSuccess;
  const current: SetupStep = step ?? (stay ? 'secure' : progress.next);
  // Step 1 needs only who is signed in: the settings an admin session starts reading must not hide
  // the admin key that is shown once.
  const pending = setup.isPending || (current === 'secure' ? progress.sessionPending : progress.pending);
  const showLoading = useDelayedLoading(pending);
  const done: Record<SetupStep, boolean> = { secure: progress.secured, connect: progress.canTry, try: false };
  const go = (to: SetupStep) => void navigate({ to: '/app/setup', search: { step: to } });

  let nextReason: Reason | null = null;
  if (current === 'secure') {
    if (!auth.signedIn) nextReason = { text: 'Sign in or create the admin key first' };
    else if (auth.createdKey && !saved) nextReason = { text: 'Tick “I saved it” first' };
    else if (!progress.admin) nextReason = 'needs_admin';
  } else if (current === 'connect' && !progress.canTry) {
    nextReason = { text: copy.setup.step_try.disabled_reason };
  }

  let body;
  if (pending) {
    body = showLoading ? <LoadingState rows={4} /> : null;
  } else if (current === 'secure') {
    body = <StepSecure auth={auth} setup={setup.data} token={token} readonly={mode.readonly} demo={mode.demo} saved={saved} onSavedChange={setSaved} />;
  } else if (current === 'connect' && progress.demo) {
    body = <StepConnectDemo />;
  } else if (!progress.admin) {
    body = (
      <div className="space-y-2">
        <p className="text-sm text-fg">
          {progress.demo
            ? 'Sign in as an admin first: running a check from the dashboard needs the admin key.'
            : 'Sign in as an admin first: connecting keys changes how every answer is checked.'}
        </p>
        <Link to="/app/setup" search={{ step: 'secure' }} className="inline-flex items-center gap-1 text-sm font-medium text-fg underline underline-offset-2">
          Back to {TITLES.secure}
          <ArrowRight aria-hidden="true" className="size-3.5" />
        </Link>
      </div>
    );
  } else if (current === 'connect') {
    body = <StepConnect progress={progress} writeReason={writeReason} />;
  } else {
    body = <StepSeeItWork progress={progress} writeReason={writeReason} />;
  }

  const index = SETUP_STEPS.indexOf(current);
  const previous = index > 0 ? SETUP_STEPS[index - 1] : null;
  const following = index < SETUP_STEPS.length - 1 ? SETUP_STEPS[index + 1] : null;

  return (
    <div className="max-w-3xl space-y-5">
      <PageHeader
        description={
          mode.demo
            ? 'On the demo: secure this dashboard, then watch TrustAgent catch a wrong answer. The demo already runs simulated checks, so there is nothing to connect.'
            : 'Three steps to a working TrustAgent: secure this dashboard, connect a key, then watch it catch a wrong answer.'
        }
      />
      <Stepper current={current} done={done} locked={current === 'secure' && auth.createdKey !== null && !saved} />
      <Card>
        <CardHeader level={2} title={`${index + 1}. ${TITLES[current]}`} />
        <CardContent>{body}</CardContent>
        {pending ? null : (
          <CardFooter className="flex flex-wrap items-center justify-between gap-3">
            {previous ? (
              <Button variant="ghost" size="sm" onClick={() => go(previous)}>
                <ArrowLeft aria-hidden="true" />
                Back
              </Button>
            ) : (
              <span />
            )}
            {following ? (
              <DisabledAction reason={nextReason}>
                {(p) => (
                  <Button variant="primary" onClick={() => go(following)} {...p}>
                    Next
                    <ArrowRight aria-hidden="true" />
                  </Button>
                )}
              </DisabledAction>
            ) : (
              <Link to="/app/overview" className="inline-flex items-center gap-1 text-sm font-medium text-fg underline underline-offset-2">
                Go to Overview
                <ArrowRight aria-hidden="true" className="size-3.5" />
              </Link>
            )}
          </CardFooter>
        )}
      </Card>
      <p className="text-xs text-fg-muted">Keys are stored encrypted here and sent only to the provider they belong to; only masked keys are ever shown.</p>
    </div>
  );
}
