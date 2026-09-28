/**
 * Toasts in the house voice (rendered by <Toaster> from src/components/ui/toast.tsx).
 *
 * - `success`: confirms what the viewer just did; fades after 4 s (paused on hover and focus).
 * - `error`: the server's own message, with its code as the description; stays until dismissed.
 * - `info`: a background change worth knowing about ("Policy reloaded from policies.yaml").
 */
import { toast, type ExternalToast } from 'sonner';
import { describeError } from './describeError';

type Options = Pick<ExternalToast, 'id' | 'description' | 'action' | 'duration'>;

export const notify = {
  success(message: string, options: Options = {}) {
    return toast.success(message, { duration: 4000, ...options });
  },
  info(message: string, options: Options = {}) {
    return toast.info(message, { duration: 6000, ...options });
  },
  /** "Couldn't save the policy" + the server's message and code. */
  error(title: string, error: unknown, options: Options = {}) {
    const { message, code } = describeError(error);
    return toast.error(title, {
      duration: Number.POSITIVE_INFINITY,
      description: code ? `${message} (${code})` : message,
      ...options,
    });
  },
  dismiss(id?: string | number) {
    toast.dismiss(id);
  },
};
