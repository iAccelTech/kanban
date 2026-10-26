// Small DOM helpers shared by every page. User text always goes in as text
// nodes; the only HTML ever inserted is Markdown the server already sanitized.

export function el(tag, props, ...children) {
  const node = document.createElement(tag);
  if (props) {
    for (const [k, v] of Object.entries(props)) {
      if (v == null || v === false) continue;
      if (k === 'class') node.className = v;
      else if (k === 'html') node.innerHTML = v; // server-sanitized HTML only
      else if (k === 'dataset') Object.assign(node.dataset, v);
      else if (k.startsWith('on') && typeof v === 'function') node.addEventListener(k.slice(2), v);
      else if (k === 'value' || k === 'checked' || k === 'disabled') node[k] = v;
      else node.setAttribute(k, v === true ? '' : v);
    }
  }
  for (const c of children.flat(Infinity)) {
    if (c == null || c === false) continue;
    node.append(c instanceof Node ? c : String(c));
  }
  return node;
}

const ICONS = {
  star: '<path d="M8 1.8l1.9 3.9 4.3.6-3.1 3 .7 4.3L8 11.6l-3.8 2 .7-4.3-3.1-3 4.3-.6z"/>',
  starFilled: '<path fill="currentColor" d="M8 1.8l1.9 3.9 4.3.6-3.1 3 .7 4.3L8 11.6l-3.8 2 .7-4.3-3.1-3 4.3-.6z"/>',
  clock: '<circle cx="8" cy="8" r="6"/><path d="M8 4.5V8l2.5 1.5"/>',
  desc: '<path d="M3 4h10M3 8h10M3 12h6"/>',
  comment: '<path d="M2.5 3.5h11v7h-6l-3 2.5v-2.5h-2z"/>',
  check: '<rect x="2.5" y="2.5" width="11" height="11" rx="2"/><path d="M5.5 8l2 2 3.5-4"/>',
  close: '<path d="M4 4l8 8M12 4l-8 8"/>',
  dots: '<circle cx="3.5" cy="8" r="1.2" fill="currentColor"/><circle cx="8" cy="8" r="1.2" fill="currentColor"/><circle cx="12.5" cy="8" r="1.2" fill="currentColor"/>',
  plus: '<path d="M8 3v10M3 8h10"/>',
  activity: '<path d="M2 8h3l2-4.5 2 9 2-4.5h3"/>',
  tag: '<path d="M2.5 2.5h5l6 6-5 5-6-6z"/><circle cx="5.5" cy="5.5" r="1" fill="currentColor"/>',
  archive: '<rect x="2" y="3" width="12" height="3" rx="1"/><path d="M3 6v7h10V6M6.5 9h3"/>',
  card: '<rect x="2" y="3" width="12" height="10" rx="2"/><path d="M5 7h6"/>',
  pencil: '<path d="M10.5 2.5l3 3-8 8h-3v-3z"/>',
  board: '<rect x="2" y="2.5" width="12" height="11" rx="2"/><path d="M6 2.5v11M10 2.5v7"/>',
  image: '<rect x="2" y="3" width="12" height="10" rx="2"/><path d="M2 11l3.5-3.5 3 3 2-2L14 12"/>',
};

export function icon(name) {
  const span = document.createElement('span');
  span.className = 'icon';
  span.innerHTML = `<svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor"
    stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS[name]}</svg>`;
  return span;
}

// ---- toasts ----

export function toast(message, kind = 'error') {
  let box = document.getElementById('toasts');
  if (!box) {
    box = el('div', { id: 'toasts' });
    document.body.append(box);
  }
  const t = el('div', { class: `toast toast-${kind}`, role: 'status' }, message);
  box.append(t);
  setTimeout(() => t.classList.add('toast-out'), 4000);
  setTimeout(() => t.remove(), 4400);
}

// ---- popovers ----

let openPop = null;

export function isPopoverOpen() {
  return openPop !== null;
}

export function closePopover() {
  if (openPop) {
    openPop.remove();
    openPop = null;
  }
}

// popover shows a small panel under anchor. build(close) returns its content and
// is called again by the returned refresh().
export function popover(anchor, title, build) {
  closePopover();
  const pop = el('div', { class: 'popover', role: 'dialog', 'aria-label': title });
  const refresh = () => {
    pop.replaceChildren(
      el('div', { class: 'popover-head' },
        el('span', {}, title),
        el('button', { class: 'icon-btn', title: 'Close', onclick: closePopover }, icon('close'))),
      el('div', { class: 'popover-body' }, build(closePopover, refresh)));
  };
  refresh();
  document.body.append(pop);
  openPop = pop;

  const r = anchor.getBoundingClientRect();
  const w = pop.offsetWidth;
  const left = Math.max(8, Math.min(r.left, window.innerWidth - w - 8));
  let top = r.bottom + 6;
  if (top + pop.offsetHeight > window.innerHeight - 8) top = Math.max(8, r.top - pop.offsetHeight - 6);
  pop.style.left = `${left + window.scrollX}px`;
  pop.style.top = `${top + window.scrollY}px`;
  pop.querySelector('input:not([type=checkbox]), textarea')?.focus();
  return { close: closePopover, refresh };
}

document.addEventListener('mousedown', (e) => {
  if (openPop && !openPop.contains(e.target) && !e.target.closest('[data-pop-anchor]')) closePopover();
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && openPop) {
    e.stopImmediatePropagation();
    closePopover();
  }
}, true);

// ---- inline editing ----

// inlineEdit swaps target for a text field. Enter or blur saves (when the value
// changed), Escape cancels.
export function inlineEdit(target, { value, multiline = false, maxlength = 200, onSave }) {
  const input = el(multiline ? 'textarea' : 'input', {
    class: 'inline-input', value, maxlength, rows: multiline ? 2 : null,
  });
  let done = false;
  const finish = async (save) => {
    if (done) return;
    done = true;
    const v = input.value.trim();
    input.remove();
    target.hidden = false;
    if (save && v && v !== value) {
      try {
        await onSave(v);
      } catch (err) {
        toast(err.message);
      }
    }
  };
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      finish(true);
    } else if (e.key === 'Escape') {
      e.stopPropagation();
      finish(false);
    }
  });
  input.addEventListener('blur', () => finish(true));
  target.hidden = true;
  target.after(input);
  input.focus();
  input.select();
  return input;
}

// ---- dates ----

export function fmtDate(ms, withTime = false) {
  const d = new Date(ms);
  const opts = { month: 'short', day: 'numeric' };
  if (d.getFullYear() !== new Date().getFullYear()) opts.year = 'numeric';
  let s = d.toLocaleDateString(undefined, opts);
  if (withTime) s += ', ' + d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
  return s;
}

export function dueStatus(card) {
  if (card.due == null) return '';
  if (card.due_done) return 'due-done';
  const left = card.due - Date.now();
  if (left < 0) return 'due-overdue';
  if (left < 24 * 3600 * 1000) return 'due-soon';
  return '';
}

export function relTime(ms) {
  const s = Math.round((Date.now() - ms) / 1000);
  if (s < 45) return 'just now';
  const m = Math.round(s / 60);
  if (m < 60) return m === 1 ? '1 minute ago' : `${m} minutes ago`;
  const h = Math.round(m / 60);
  if (h < 24) return h === 1 ? '1 hour ago' : `${h} hours ago`;
  return fmtDate(ms, true);
}

export function toLocalInput(ms) {
  const d = new Date(ms);
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

export const BOARD_COLORS = ['blue', 'orange', 'green', 'red', 'purple', 'pink', 'lime', 'sky', 'grey'];
export const LABEL_COLORS = ['green', 'yellow', 'orange', 'red', 'purple', 'blue', 'sky', 'lime', 'pink', 'black'];

export function byRank(a, b) {
  if (a.rank < b.rank) return -1;
  if (a.rank > b.rank) return 1;
  return a.id - b.id;
}
