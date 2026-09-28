/**
 * The presets picker (SPEC §2.7 presets, contracts/policies/presets.v1.json): Observe everything,
 * Flag risky answers, Block contradictions. A preset is applied to one route of the draft; the
 * viewer still reviews and saves it like any other change.
 */
import { LayoutTemplate } from 'lucide-react';
import { useState } from 'react';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogTrigger } from '@/components/ui/dialog';
import { Field } from '@/components/ui/field';
import { RadioGroup } from '@/components/ui/radio-group';
import { Select } from '@/components/ui/select';
import { PRESETS, type PolicyPreset } from './policyDraft';
import { routeTitle } from './routeWords';

export interface PresetsDialogProps {
  routes: readonly string[];
  writeReason: Reason | null;
  onApply: (route: string, preset: PolicyPreset) => void;
}

export function PresetsDialog({ routes, writeReason, onApply }: PresetsDialogProps) {
  const [open, setOpen] = useState(false);
  const [presetId, setPresetId] = useState<string>(PRESETS[0]?.id ?? '');
  const [route, setRoute] = useState<string>(routes[1] ?? routes[0] ?? 'default');
  const preset = PRESETS.find((p) => p.id === presetId);
  const target = routes.includes(route) ? route : (routes[0] ?? 'default');

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DisabledAction reason={writeReason}>
        {(p) => (
          <DialogTrigger
            render={
              <Button {...p}>
                <LayoutTemplate aria-hidden="true" />
                Presets
              </Button>
            }
          />
        )}
      </DisabledAction>
      <DialogContent
        title="Start from a preset"
        description="A preset sets the mode and the settings that go with it on one route. You review the change before it is saved."
        footer={
          <>
            <Button variant="secondary" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="primary"
              onClick={() => {
                if (!preset) return;
                onApply(target, preset);
                setOpen(false);
              }}
            >
              Apply to draft
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <RadioGroup
            label="Preset"
            value={presetId}
            onValueChange={setPresetId}
            options={PRESETS.map((p) => ({
              value: p.id,
              label: p.name,
              description: p.ui_note ? `${p.description} ${p.ui_note}.` : p.description,
            }))}
          />
          <Field label="Route">
            <Select
              value={target}
              onValueChange={(v) => v && setRoute(v)}
              options={routes.map((r) => ({ value: r, label: routeTitle(r), description: r === 'default' ? 'Requests no other route matches' : undefined }))}
            />
          </Field>
        </div>
      </DialogContent>
    </Dialog>
  );
}
