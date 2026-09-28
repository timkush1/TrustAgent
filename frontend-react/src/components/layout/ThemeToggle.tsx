import { Monitor, Moon, Sun } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Tooltip } from '@/components/ui/tooltip';
import { nextThemePreference, setThemePreference, useThemePreference, type ThemePreference } from '@/lib/theme';

const meta: Record<ThemePreference, { label: string; icon: typeof Sun }> = {
  light: { label: 'Light', icon: Sun },
  dark: { label: 'Dark', icon: Moon },
  system: { label: 'System', icon: Monitor },
};

/** Cycles light → dark → system. */
export function ThemeToggle() {
  const pref = useThemePreference();
  const next = nextThemePreference(pref);
  const { icon: Icon, label } = meta[pref];
  const text = `Theme: ${label}. Switch to ${meta[next].label}`;
  return (
    <Tooltip label={text}>
      <Button variant="ghost" size="icon" aria-label={text} onClick={() => setThemePreference(next)}>
        <Icon aria-hidden="true" />
      </Button>
    </Tooltip>
  );
}
