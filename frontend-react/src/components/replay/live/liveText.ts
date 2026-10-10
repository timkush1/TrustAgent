/**
 * The live surfaces' own sentences: headings, field labels, figure bases and short states that
 * contracts/ui copy.playground_live.* (D136) does not carry; every sentence the contract has
 * (consent, errors, callback states, caps) comes from it instead. None names
 * an env var, a settings page or a make target a visitor does not have. Kept beside the components
 * that show them, outside src/domain/words (CO-6: a words module takes its sentences from the copy
 * contract). Owned by WEB-LIVE (LV-6).
 */
export const liveText = {
  section_title: 'Check an answer live, with your own key',
  section_intro:
    'Run the fast check (Jev) on one of these examples or on your own text. It runs in this tab and calls OpenRouter with your key; nothing goes through a TrustAgent server.',
  pick_label: 'What to check',
  custom_title: 'Your own text',
  custom_hint: 'Checked on the support-bot route: is the answer backed by your documents, or does it contradict them?',
  documents_label: 'Documents the answer must stick to',
  question_label: 'The question (optional)',
  answer_label: 'The answer to check',
  documents_missing: 'Add the documents the answer must stick to.',
  answer_missing: 'Add the answer to check.',
  too_long: (max: number) => `Keep this under ${max.toLocaleString('en-US')} characters.`,
  shorten: 'Shorten the text marked above to run the check.',
  no_call: 'No call to OpenRouter: the checks on this route skip this text (the result says why).',
  run_yourself: 'Run it yourself',
  asked: 'The user asked',
  answered: 'The assistant answered',
  connect_first: 'Connect a key first: nothing is sent to OpenRouter until you agree.',
  running: 'Asking Jev on OpenRouter…',
  key_in_tab: 'Your OpenRouter key is in this tab',
  key_in_memory: 'Your OpenRouter key is in memory only (this browser blocks storage)',
  key_chip: 'Your key',
  forgotten: 'Key forgotten.',
  spent_unknown: 'Spent so far: not known (OpenRouter reported no cost and the price table has none)',
  this_check: 'This check',
  check_cost_subject: 'The cost of this check',
  spent_subject: 'What this tab has spent on OpenRouter',
  per_check_subject: 'One fast check',
  worst_case_subject: 'The most these caps allow',
  estimate_subject: 'This check, before you run it',
  estimate_basis: 'The tokens this page counts in these requests before sending them, at the Jev input price in the price table (Jev bills input only).',
  worst_case_basis:
    'The call cap times the dearer of one Jev call and one drafting call at the per-call token cap, at the price-table prices (all of it as output for the drafting model, so it is never understated).',
  vendor_basis: 'The cost OpenRouter reported in the response (usage.cost).',
  computed_basis: 'Tokens times the price table, because OpenRouter reported no cost for at least one call.',
  show_request: 'Show exact Jev request',
  consent_title: 'Run the fast check with your OpenRouter key',
  consent_intro: 'Before anything is sent:',
  paste_label: 'Your OpenRouter key',
  paste_help: 'It starts with sk-or-. Create one with a small credit limit at openrouter.ai/settings/keys.',
  paste_save: 'Use this key',
  paste_checking: 'Checking the key…',
  back_to_playground: 'Back to the Playground',
  callback_title: 'Connect OpenRouter',
  connect_off_paste: 'Connect is off in this build; paste a key instead.',
  no_credits_title: 'Out of credits',
  no_credits_checking: 'Checking the key…',
  no_credits_still: 'The key still has no credit left.',
  unavailable_title: 'Live checks are off on this site',
} as const;
