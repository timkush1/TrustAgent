/**
 * Text that marks code with backticks, as check instructions do ("Does `assistant_response`
 * state …"): each backticked part renders as code, so no literal backtick reaches the reader. The
 * landing and the dashboard's glass box share it.
 */
export function Ticked({ text }: { text: string }) {
  return (
    <>
      {text.split('`').map((part, i) =>
        i % 2 === 1 ? (
          <code key={i} className="font-mono text-[0.85em] text-fg">
            {part}
          </code>
        ) : (
          <span key={i}>{part}</span>
        ),
      )}
    </>
  );
}
