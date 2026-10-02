/**
 * Thrown from a ConfirmDialog's `onConfirm` when the server asks for a second, informed
 * confirmation (D77: `409 confirmation_required` names the routes that fail closed). The
 * dialog stays open and shows no failure: the caller has already changed what the dialog says and
 * its button ("Remove anyway"), so the viewer answers the new question.
 */
export class ConfirmAgain extends Error {
  constructor() {
    super('The server asks to confirm again.');
    this.name = 'ConfirmAgain';
  }
}
