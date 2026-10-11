// The console's only script of its own. Everything works without it: pages are server-rendered
// and forms post normally. It adds three things:
//   - a confirmation dialog for forms marked data-confirm (the text says what will change),
//     whether the form posts normally or through htmx (a button in a list);
//   - canRefresh(), which htmx asks before re-reading a page on its timer, so a page is never
//     replaced while you have a form open or are typing;
//   - sections you opened (details with an id: they only show state) stay open when a page is
//     re-read;
//   - Generate on a channel's address field: a random ntfy topic made in this browser, with its
//     subscribe link. It goes nowhere until the form is submitted.
window.canRefresh = () =>
  document.visibilityState === 'visible'
  && !document.querySelector('main details[open]:not([id]), dialog[open]')
  && !(document.activeElement && document.activeElement.matches('input, select, textarea'));

function ask(form, proceed) {
  const dialog = document.getElementById('confirm');
  if (!dialog || !dialog.showModal) return proceed(); // no dialog support: go ahead as a plain form would
  dialog.querySelector('p').textContent = form.dataset.confirm;
  dialog.querySelector('button.danger').textContent = form.dataset.confirmLabel || 'Continue';
  dialog.returnValue = '';
  dialog.querySelectorAll('button').forEach((button) => { button.onclick = () => dialog.close(button.value); });
  dialog.addEventListener('close', () => { if (dialog.returnValue === 'yes') proceed(); }, { once: true });
  dialog.showModal();
}

document.addEventListener('submit', (event) => {
  const form = event.target;
  if (!(form instanceof HTMLFormElement) || !form.dataset.confirm || form.dataset.confirmed) return;
  if (window.htmx && form.hasAttribute('hx-post')) return; // htmx sends it: asked in htmx:confirm below
  event.preventDefault();
  ask(form, () => {
    form.dataset.confirmed = 'yes';
    form.requestSubmit(event.submitter || undefined);
  });
});

document.addEventListener('htmx:confirm', (event) => {
  const form = event.detail.elt;
  if (!(form instanceof HTMLFormElement) || !form.dataset.confirm) return;
  event.preventDefault();
  ask(form, () => event.detail.issueRequest(true));
});

let opened = [];
document.addEventListener('htmx:beforeSwap', (event) => {
  if (event.detail.target.tagName === 'MAIN') opened = [...document.querySelectorAll('main details[open][id]')].map((d) => d.id);
});
document.addEventListener('htmx:afterSwap', (event) => {
  if (event.detail.target.tagName !== 'MAIN') return;
  opened.forEach((id) => { const section = document.getElementById(id); if (section) section.open = true; });
});

function generateButtons(root) {
  root.querySelectorAll('[data-address]').forEach((field) => {
    const button = field.querySelector('[data-generate]');
    if (!button || !window.crypto || button.dataset.ready) return;
    button.dataset.ready = 'yes';
    button.hidden = false; // only offered where the browser can make the random topic
    button.addEventListener('click', () => {
      const bytes = crypto.getRandomValues(new Uint8Array(12));
      const address = 'https://ntfy.sh/swhurl-' + [...bytes].map((b) => b.toString(16).padStart(2, '0')).join('');
      field.querySelector('input').value = address;
      const link = field.querySelector('[data-subscribe]');
      link.href = address;
      link.textContent = address;
      field.querySelector('[data-generated]').hidden = false;
    });
    field.querySelector('input').addEventListener('input', () => { field.querySelector('[data-generated]').hidden = true; });
  });
}
generateButtons(document);
document.addEventListener('htmx:afterSwap', (event) => generateButtons(event.detail.target));
