// The console's only script of its own. Everything works without it: pages are server-rendered
// and forms post normally. It adds two things:
//   - a confirmation dialog for forms marked data-confirm (the text says what will change);
//   - canRefresh(), which htmx asks before re-reading a page on its timer, so a page is never
//     replaced while you have something open or are typing.
window.canRefresh = () =>
  document.visibilityState === 'visible'
  && !document.querySelector('main details[open], dialog[open]')
  && !(document.activeElement && document.activeElement.matches('input, select, textarea'));

document.addEventListener('submit', (event) => {
  const form = event.target;
  if (!(form instanceof HTMLFormElement) || !form.dataset.confirm || form.dataset.confirmed) return;
  const dialog = document.getElementById('confirm');
  if (!dialog || !dialog.showModal) return; // no dialog support: submit as a plain form
  event.preventDefault();
  dialog.querySelector('p').textContent = form.dataset.confirm;
  dialog.querySelector('button.danger').textContent = form.dataset.confirmLabel || 'Continue';
  dialog.returnValue = '';
  dialog.querySelectorAll('button').forEach((button) => { button.onclick = () => dialog.close(button.value); });
  dialog.addEventListener('close', () => {
    if (dialog.returnValue !== 'yes') return;
    form.dataset.confirmed = 'yes';
    form.requestSubmit(event.submitter || undefined);
  }, { once: true });
  dialog.showModal();
});
