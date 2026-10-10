/**
 * The consent dialog before any call to openrouter.ai (D114): the four lines, the cost chip, the
 * worst case with these caps, Connect (PKCE) and Paste a key. Owned by WEB-LIVE (LV-6).
 *
 * Step 0b stub: the frozen props; renders nothing.
 */
export interface ConsentDialogProps {
  open: boolean;
  onClose: () => void;
  /** Where the PKCE callback returns to (router-relative, D105), e.g. `app/playground/#live`. */
  returnTo: string;
}

export function ConsentDialog(_props: ConsentDialogProps) {
  return null;
}
