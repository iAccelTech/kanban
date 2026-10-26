// The card detail modal.
import { api } from './api.js';
import {
  el, icon, toast, popover, closePopover, isPopoverOpen, inlineEdit,
  fmtDate, dueStatus, relTime, toLocalInput, LABEL_COLORS,
} from './dom.js';

const ME = 1; // the built-in user, until login exists

let deps = null; // { state, upsertCard, upsertLabel, removeLabel } from board.js
let cur = null; // { id, detail, editingDesc, descVersion, addingItem:Set, editingComment }
const drafts = new Map(); // unsaved text by data-key, kept across re-renders
let root = null;
let loadToken = 0;

export function setupCardModal(d) {
  deps = d;
  root = document.getElementById('modal-root');
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape' || !cur || isPopoverOpen()) return;
    if (e.target.closest?.('.modal textarea, .modal input')) return;
    closeCard();
  });
}

export function currentCardId() {
  return cur?.id ?? null;
}

export function openCard(id) {
  if (location.hash !== `#card-${id}`) {
    location.hash = `card-${id}`; // the hashchange handler calls back in
    return;
  }
  if (cur?.id === id) return;
  drafts.clear();
  cur = { id, detail: null, editingDesc: false, descVersion: 0, addingItem: new Set(), editingComment: 0 };
  root.replaceChildren(el('div', { class: 'modal-backdrop' }, el('div', { class: 'modal modal-loading' }, 'Loading…')));
  document.body.classList.add('modal-open');
  refreshCard();
}

export function closeCard() {
  if (!cur) return;
  cur = null;
  drafts.clear();
  closePopover();
  root.replaceChildren();
  document.body.classList.remove('modal-open');
  if (location.hash) history.replaceState(null, '', location.pathname + location.search);
}

export async function refreshCard() {
  if (!cur) return;
  const id = cur.id;
  const token = ++loadToken;
  try {
    const d = await api('GET', `/cards/${id}`);
    if (cur?.id !== id || token !== loadToken) return;
    cur.detail = d;
    render();
  } catch (err) {
    if (cur?.id !== id) return;
    if (err.status === 404) {
      toast('That card no longer exists.');
      closeCard();
    } else {
      toast(err.message);
    }
  }
}

function setDetail(d) {
  if (!cur || cur.id !== d.id) return;
  cur.detail = d;
  deps.upsertCard(d);
  render();
}

async function patch(body) {
  try {
    setDetail(await api('PATCH', `/cards/${cur.id}`, body));
    return true;
  } catch (err) {
    toast(err.message);
    return false;
  }
}

async function call(method, path, body) {
  try {
    setDetail(await api(method, path, body));
    return true;
  } catch (err) {
    toast(err.message);
    return false;
  }
}

// draftField is a text field whose unsaved value survives re-renders.
function draftField(tag, key, initial, props = {}) {
  const field = el(tag, { ...props, dataset: { key }, value: drafts.has(key) ? drafts.get(key) : initial });
  field.addEventListener('input', () => drafts.set(key, field.value));
  return field;
}

function section(iconName, title, ...rest) {
  return el('section', { class: 'm-section' },
    el('div', { class: 'm-section-head' }, icon(iconName), el('h3', {}, title), ...rest));
}

// ---- render ----

function render() {
  if (!cur?.detail) return;
  const d = cur.detail;
  const list = deps.state.lists.get(d.list_id);

  // Keep focus and caret where they were.
  const active = document.activeElement;
  const focusKey = root.contains(active) ? active?.dataset?.key : null;
  const sel = focusKey ? [active.selectionStart, active.selectionEnd] : null;

  const title = draftField('textarea', 'title', d.title, {
    class: 'm-title', rows: 1, maxlength: 512, 'aria-label': 'Card title', spellcheck: 'true',
  });
  const saveTitle = async () => {
    // Compare with the latest detail, not this render's: a re-render can blur
    // the old field after the save already landed.
    const v = title.value.replace(/\s+/g, ' ').trim();
    drafts.delete('title');
    if (!cur?.detail) return;
    if (!v) {
      title.value = cur.detail.title;
      return;
    }
    if (v !== cur.detail.title) await patch({ title: v });
  };
  title.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      title.blur();
    } else if (e.key === 'Escape') {
      e.stopPropagation();
      drafts.delete('title');
      title.value = d.title;
      title.blur();
    }
  });
  title.addEventListener('blur', saveTitle);
  title.addEventListener('input', () => autosize(title));

  const modal = el('div', { class: 'modal', role: 'dialog', 'aria-modal': 'true', 'aria-label': d.title },
    el('button', { class: 'icon-btn modal-close', title: 'Close', onclick: closeCard }, icon('close')),
    d.archived_at ? el('div', { class: 'm-archived' }, icon('archive'), 'This card is archived.') : null,
    el('header', { class: 'm-header' }, icon('card'),
      el('div', { class: 'm-header-text' }, title,
        el('p', { class: 'muted' }, 'in list ', el('strong', {}, list ? list.title : 'an archived list')))),
    el('div', { class: 'm-body' },
      el('div', { class: 'm-main' }, metaRow(d), descriptionSection(d), d.checklists.map(checklistSection), commentsSection(d)),
      sidebar(d)));

  const backdrop = el('div', {
    class: 'modal-backdrop',
    onmousedown: (e) => { if (e.target === backdrop) closeCard(); },
  }, modal);
  root.replaceChildren(backdrop);
  autosize(title);

  if (focusKey) {
    const n = root.querySelector(`[data-key="${CSS.escape(focusKey)}"]`);
    if (n) {
      n.focus();
      try {
        n.setSelectionRange(...sel);
      } catch {
        // not a text field
      }
    }
  }
}

function autosize(ta) {
  ta.style.height = 'auto';
  ta.style.height = `${ta.scrollHeight}px`;
}

function metaRow(d) {
  const items = [];
  const labels = d.label_ids.map((id) => deps.state.labels.get(id)).filter(Boolean).sort((a, b) => a.id - b.id);
  if (labels.length) {
    const add = el('button', { class: 'chip-btn', title: 'Edit labels', 'data-pop-anchor': '' }, icon('plus'));
    add.onclick = () => labelsPopover(add);
    items.push(el('div', { class: 'meta' }, el('h4', {}, 'Labels'),
      el('div', { class: 'meta-chips' },
        labels.map((l) => el('span', { class: `label-chip lg lc-${l.color}` }, l.name)), add)));
  }
  if (d.due != null) {
    const status = dueStatus(d);
    const dateBtn = el('button', { class: `due-chip ${status}`, 'data-pop-anchor': '', title: 'Change due date' },
      fmtDate(d.due, true),
      status === 'due-done' ? el('span', { class: 'tag' }, 'Complete')
        : status === 'due-overdue' ? el('span', { class: 'tag' }, 'Overdue')
          : status === 'due-soon' ? el('span', { class: 'tag' }, 'Due soon') : null);
    dateBtn.onclick = () => datesPopover(dateBtn);
    items.push(el('div', { class: 'meta' }, el('h4', {}, 'Due date'),
      el('div', { class: 'meta-chips' },
        el('input', {
          type: 'checkbox', checked: d.due_done, title: 'Mark complete', class: 'check',
          onchange: (e) => patch({ due_done: e.target.checked }),
        }), dateBtn)));
  }
  return items.length ? el('div', { class: 'meta-row' }, items) : null;
}

function descriptionSection(d) {
  if (cur.editingDesc) {
    const ta = draftField('textarea', 'desc', d.description, {
      class: 'm-textarea', rows: 8, maxlength: 16384,
      placeholder: 'Add a more detailed description… Markdown works: **bold**, - lists, [links](https://…)',
    });
    const cancel = () => {
      cur.editingDesc = false;
      drafts.delete('desc');
      render();
    };
    const save = async () => {
      const description = ta.value;
      try {
        let res;
        try {
          res = await api('PATCH', `/cards/${cur.id}`, { description, version: cur.descVersion });
        } catch (err) {
          if (err.status !== 409) throw err;
          if (!confirm('Someone changed this card while you were editing. Save your description anyway?')) return;
          res = await api('PATCH', `/cards/${cur.id}`, { description });
        }
        cur.editingDesc = false;
        drafts.delete('desc');
        setDetail(res);
      } catch (err) {
        toast(err.message);
      }
    };
    ta.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        cancel();
      } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        save();
      }
    });
    queueMicrotask(() => { if (document.activeElement === document.body) ta.focus(); });
    return section('desc', 'Description',
      el('div', { class: 'm-editor' }, ta,
        el('div', { class: 'composer-actions' },
          el('button', { class: 'btn primary', onclick: save }, 'Save'),
          el('button', { class: 'btn ghost', onclick: cancel }, 'Cancel'),
          el('span', { class: 'muted small' }, 'Markdown supported · Ctrl+Enter to save'))));
  }
  const edit = () => {
    cur.editingDesc = true;
    cur.descVersion = cur.detail.version;
    render();
    root.querySelector('[data-key="desc"]')?.focus();
  };
  const body = d.description_html
    ? el('div', {
      class: 'md', html: d.description_html, title: 'Click to edit',
      onclick: (e) => { if (!e.target.closest('a')) edit(); },
    })
    : el('button', { class: 'placeholder', onclick: edit }, 'Add a more detailed description…');
  const s = section('desc', 'Description',
    d.description_html ? el('button', { class: 'btn small', onclick: edit }, 'Edit') : null);
  s.append(body);
  return s;
}

function checklistSection(k) {
  const total = k.items.length;
  const done = k.items.filter((i) => i.done).length;
  const pct = total ? Math.round((done / total) * 100) : 0;

  const title = el('h3', { class: 'editable', title: 'Rename checklist' }, k.title);
  title.onclick = () => inlineEdit(title, {
    value: k.title,
    onSave: (v) => call('PATCH', `/checklists/${k.id}`, { title: v }),
  });
  const del = el('button', {
    class: 'btn small',
    onclick: () => { if (confirm(`Delete checklist "${k.title}"?`)) call('DELETE', `/checklists/${k.id}`); },
  }, 'Delete');

  const items = k.items.map((it) => {
    const text = el('span', { class: 'ci-text' + (it.done ? ' done' : ''), title: 'Edit item' }, it.text);
    text.onclick = () => inlineEdit(text, {
      value: it.text, maxlength: 512,
      onSave: (v) => call('PATCH', `/items/${it.id}`, { text: v }),
    });
    return el('li', { class: 'ci' },
      el('input', {
        type: 'checkbox', class: 'check', checked: it.done, 'aria-label': it.text,
        onchange: (e) => call('PATCH', `/items/${it.id}`, { done: e.target.checked }),
      }),
      text,
      el('button', {
        class: 'icon-btn ci-del', title: 'Delete item', onclick: () => call('DELETE', `/items/${it.id}`),
      }, icon('close')));
  });

  let adder;
  const key = `item-${k.id}`;
  if (cur.addingItem.has(k.id)) {
    const input = draftField('input', key, '', { class: 'composer-input', maxlength: 512, placeholder: 'Add an item' });
    const stop = () => {
      cur.addingItem.delete(k.id);
      drafts.delete(key);
      render();
    };
    const add = async () => {
      const text = input.value.trim();
      if (!text) return input.focus();
      drafts.delete(key);
      input.value = '';
      if (!(await call('POST', `/checklists/${k.id}/items`, { text }))) drafts.set(key, text);
      root.querySelector(`[data-key="${key}"]`)?.focus();
    };
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        add();
      } else if (e.key === 'Escape') {
        e.stopPropagation();
        stop();
      }
    });
    adder = el('div', { class: 'ci-adder' }, input,
      el('div', { class: 'composer-actions' },
        el('button', { class: 'btn primary', onclick: add }, 'Add'),
        el('button', { class: 'btn ghost', onclick: stop }, 'Cancel')));
  } else {
    adder = el('button', {
      class: 'btn small',
      onclick: () => {
        cur.addingItem.add(k.id);
        render();
        root.querySelector(`[data-key="${key}"]`)?.focus();
      },
    }, 'Add an item');
  }

  const s = el('section', { class: 'm-section' },
    el('div', { class: 'm-section-head' }, icon('check'), title, el('span', { class: 'spacer' }), del),
    el('div', { class: 'progress' },
      el('span', { class: 'muted small' }, `${pct}%`),
      el('div', { class: 'bar' + (total && done === total ? ' complete' : '') }, el('div', { style: `width:${pct}%` }))),
    el('ul', { class: 'ci-list' }, items),
    adder);
  return s;
}

function commentsSection(d) {
  const ta = draftField('textarea', 'comment', '', { class: 'm-textarea', rows: 2, placeholder: 'Write a comment…', maxlength: 16384 });
  const save = el('button', { class: 'btn primary', disabled: !(drafts.get('comment') || '').trim() }, 'Save');
  ta.addEventListener('input', () => { save.disabled = !ta.value.trim(); });
  const submit = async () => {
    const body = ta.value.trim();
    if (!body) return;
    save.disabled = true;
    try {
      const res = await api('POST', `/cards/${cur.id}/comments`, { body });
      drafts.delete('comment');
      setDetail(res);
    } catch (err) {
      toast(err.message);
      save.disabled = false;
    }
  };
  save.onclick = submit;
  ta.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      submit();
    } else if (e.key === 'Escape') {
      e.stopPropagation();
      ta.blur();
    }
  });

  const comments = d.comment_list.map((m) => {
    let body;
    if (cur.editingComment === m.id) {
      const key = `comment-${m.id}`;
      const edit = draftField('textarea', key, m.body, { class: 'm-textarea', rows: 3, maxlength: 16384 });
      const stop = () => {
        cur.editingComment = 0;
        drafts.delete(key);
        render();
      };
      edit.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') {
          e.stopPropagation();
          stop();
        }
      });
      body = el('div', {}, edit, el('div', { class: 'composer-actions' },
        el('button', {
          class: 'btn primary',
          onclick: async () => {
            if (await call('PATCH', `/comments/${m.id}`, { body: edit.value })) {
              cur.editingComment = 0;
              drafts.delete(key);
              render();
            }
          },
        }, 'Save'),
        el('button', { class: 'btn ghost', onclick: stop }, 'Cancel')));
    } else {
      body = el('div', { class: 'md comment-body', html: m.body_html });
    }
    const own = m.user_id === ME && cur.editingComment !== m.id;
    return el('li', { class: 'comment' },
      el('span', { class: 'avatar' }, initials(m.user_name)),
      el('div', { class: 'comment-main' },
        el('div', {}, el('strong', {}, m.user_name), ' ',
          el('time', { class: 'muted small', datetime: new Date(m.created).toISOString() }, relTime(m.created)),
          m.edited ? el('span', { class: 'muted small' }, ' (edited)') : null),
        body,
        own ? el('div', { class: 'comment-actions' },
          el('button', {
            class: 'link',
            onclick: () => {
              cur.editingComment = m.id;
              render();
              root.querySelector(`[data-key="comment-${m.id}"]`)?.focus();
            },
          }, 'Edit'),
          ' · ',
          el('button', {
            class: 'link',
            onclick: () => { if (confirm('Delete this comment? This can’t be undone.')) call('DELETE', `/comments/${m.id}`); },
          }, 'Delete')) : null));
  });

  return el('section', { class: 'm-section' },
    el('div', { class: 'm-section-head' }, icon('comment'), el('h3', {}, 'Comments')),
    el('div', { class: 'comment-composer' }, ta, el('div', { class: 'composer-actions' }, save,
      el('span', { class: 'muted small' }, 'Markdown supported'))),
    el('ol', { class: 'comment-list' }, comments));
}

function initials(name) {
  return name.split(/\s+/).filter(Boolean).slice(0, 2).map((w) => w[0].toUpperCase()).join('');
}

function sidebar(d) {
  const btn = (iconName, text, onclick) => {
    const b = el('button', { class: 'side-btn', 'data-pop-anchor': '' }, icon(iconName), text);
    b.onclick = () => onclick(b);
    return b;
  };
  return el('aside', { class: 'm-side' },
    el('h4', {}, 'Add to card'),
    btn('tag', 'Labels', labelsPopover),
    btn('check', 'Checklist', checklistPopover),
    btn('clock', 'Dates', datesPopover),
    el('h4', {}, 'Actions'),
    d.archived_at
      ? btn('archive', 'Send to board', () => patch({ archived: false }))
      : btn('archive', 'Archive', () => patch({ archived: true })));
}

// ---- popovers ----

function labelsPopover(anchor) {
  let editing = null; // null = list, 0 = new label, id = editing that label
  popover(anchor, 'Labels', (close, refresh) => {
    const labels = [...deps.state.labels.values()].sort((a, b) => a.id - b.id);
    if (editing !== null) return labelForm(editing ? deps.state.labels.get(editing) : null, () => {
      editing = null;
      refresh();
    });
    const on = new Set(cur.detail.label_ids);
    return el('div', { class: 'stack' },
      el('ul', { class: 'label-picker' }, labels.map((l) => el('li', {},
        el('button', {
          class: `label-chip lg pick lc-${l.color}` + (on.has(l.id) ? ' on' : ''),
          'aria-pressed': String(on.has(l.id)),
          onclick: async () => {
            if (await call(on.has(l.id) ? 'DELETE' : 'PUT', `/cards/${cur.id}/labels/${l.id}`)) refresh();
          },
        }, l.name || ' ', on.has(l.id) ? el('span', { class: 'tick' }, '✓') : null),
        el('button', { class: 'icon-btn', title: 'Edit label', onclick: () => { editing = l.id; refresh(); } }, icon('pencil'))))),
      el('button', { class: 'btn wide', onclick: () => { editing = 0; refresh(); } }, 'Create a new label'));
  });
}

function labelForm(label, back) {
  let color = label?.color || 'green';
  const name = el('input', { value: label?.name || '', maxlength: 64, placeholder: 'Label name (optional)' });
  const preview = el('div', { class: `label-chip lg lc-${color}` }, label?.name || ' ');
  name.addEventListener('input', () => { preview.textContent = name.value || ' '; });
  const swatches = el('div', { class: 'swatches' }, LABEL_COLORS.map((c) => el('button', {
    type: 'button', class: `swatch lc-${c}` + (c === color ? ' on' : ''), title: c,
    onclick: (e) => {
      color = c;
      preview.className = `label-chip lg lc-${c}`;
      swatches.querySelectorAll('.swatch').forEach((s) => s.classList.remove('on'));
      e.currentTarget.classList.add('on');
    },
  })));
  const save = async (e) => {
    e.preventDefault();
    try {
      const l = label
        ? await api('PATCH', `/labels/${label.id}`, { name: name.value, color })
        : await api('POST', `/boards/${deps.state.board.id}/labels`, { name: name.value, color });
      deps.upsertLabel(l);
      render();
      back();
    } catch (err) {
      toast(err.message);
    }
  };
  const remove = async () => {
    if (!confirm('Delete this label? It will be removed from every card.')) return;
    try {
      await api('DELETE', `/labels/${label.id}`);
      deps.removeLabel(label.id);
      cur.detail.label_ids = cur.detail.label_ids.filter((x) => x !== label.id);
      render();
      back();
    } catch (err) {
      toast(err.message);
    }
  };
  return el('form', { class: 'stack', onsubmit: save },
    preview,
    el('label', { class: 'field-label' }, 'Name', name),
    el('span', { class: 'field-label' }, 'Color'), swatches,
    el('div', { class: 'composer-actions' },
      el('button', { class: 'btn primary', type: 'submit' }, label ? 'Save' : 'Create'),
      el('button', { class: 'btn ghost', type: 'button', onclick: back }, 'Back'),
      label ? el('button', { class: 'btn danger', type: 'button', onclick: remove }, 'Delete') : null));
}

function checklistPopover(anchor) {
  popover(anchor, 'Add checklist', (close) => {
    const title = el('input', { value: 'Checklist', maxlength: 200 });
    return el('form', {
      class: 'stack',
      onsubmit: async (e) => {
        e.preventDefault();
        if (await call('POST', `/cards/${cur.id}/checklists`, { title: title.value })) close();
      },
    }, el('label', { class: 'field-label' }, 'Title', title), el('button', { class: 'btn primary', type: 'submit' }, 'Add'));
  });
}

function datesPopover(anchor) {
  popover(anchor, 'Dates', (close) => {
    const d = cur.detail;
    const tomorrow = new Date();
    tomorrow.setDate(tomorrow.getDate() + 1);
    tomorrow.setHours(12, 0, 0, 0);
    const input = el('input', { type: 'datetime-local', value: toLocalInput(d.due ?? tomorrow.getTime()) });
    return el('form', {
      class: 'stack',
      onsubmit: async (e) => {
        e.preventDefault();
        const ms = new Date(input.value).getTime();
        if (Number.isNaN(ms)) return toast('Pick a date and time.');
        if (await patch({ due: ms })) close();
      },
    },
    el('label', { class: 'field-label' }, 'Due date', input),
    el('div', { class: 'composer-actions' },
      el('button', { class: 'btn primary', type: 'submit' }, 'Save'),
      d.due != null ? el('button', {
        class: 'btn ghost', type: 'button',
        onclick: async () => { if (await patch({ due: null })) close(); },
      }, 'Remove') : null));
  });
}
