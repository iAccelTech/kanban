// The board page: lists, cards, drag and drop, and live updates.
import Sortable from '../vendor/sortable.esm.js';
import { api, CLIENT_ID } from './api.js';
import {
  el, icon, toast, popover, closePopover, inlineEdit, fmtDate, dueStatus, relTime, byRank, BOARD_COLORS,
} from './dom.js';
import { describe } from './activity.js';
import { setupCardModal, openCard, closeCard, currentCardId, refreshCard } from './card.js';

const boardId = Number(new URLSearchParams(location.search).get('id'));

const state = {
  board: null,
  lists: new Map(),
  cards: new Map(),
  labels: new Map(),
  members: [],
  seq: 0,
};

const $header = document.getElementById('board-header');
const $banner = document.getElementById('archived-banner');
const $lists = document.getElementById('lists');
const $activity = document.getElementById('activity');

const listEls = new Map(); // list id -> { root, cards, title }
let addListCol = null;
let events = null;
let live = false;

// While a drag or a move request is in flight, renders are deferred so the
// DOM the user is manipulating doesn't change underneath them.
let dragging = false;
let pendingMoves = 0;
const dirtyLists = new Set();
let listsOrderDirty = false;
const busy = () => dragging || pendingMoves > 0;

// ---- model updates ----

function summary(c) {
  const { description, description_html, checklists, comment_list, created, ...rest } = c;
  return rest;
}

function upsertCard(c) {
  const prev = state.cards.get(c.id);
  if (prev && prev.version > c.version) return; // stale
  if (c.archived_at || !state.lists.has(c.list_id)) state.cards.delete(c.id);
  else state.cards.set(c.id, summary(c));
  if (prev) renderCards(prev.list_id);
  if (!prev || prev.list_id !== c.list_id) renderCards(c.list_id);
}

function upsertList(l) {
  const prev = state.lists.get(l.id);
  if (prev && prev.version > l.version) return;
  if (l.archived_at) {
    if (!prev) return;
    state.lists.delete(l.id);
    for (const [id, c] of state.cards) if (c.list_id === l.id) state.cards.delete(id);
    listEls.get(l.id)?.root.remove();
    listEls.delete(l.id);
    return;
  }
  state.lists.set(l.id, l);
  if (!prev) {
    addListCol.before(buildList(l));
  } else {
    const entry = listEls.get(l.id);
    if (entry) entry.title.textContent = l.title;
  }
  placeLists();
}

function upsertLabel(label) {
  state.labels.set(label.id, label);
  renderAllCards();
}

function removeLabel(id) {
  state.labels.delete(id);
  for (const c of state.cards.values()) c.label_ids = c.label_ids.filter((x) => x !== id);
  renderAllCards();
}

function applySnapshot(snap) {
  state.board = snap.board;
  state.lists = new Map(snap.lists.map((l) => [l.id, l]));
  state.cards = new Map(snap.cards.map((c) => [c.id, c]));
  state.labels = new Map(snap.labels.map((l) => [l.id, l]));
  state.members = snap.members;
  state.seq = snap.seq;
}

// ---- rendering ----

function renderAll() {
  renderHeader();
  listEls.clear();
  addListCol = buildAddList();
  $lists.replaceChildren(...[...state.lists.values()].sort(byRank).map(buildList), addListCol);
}

function renderHeader() {
  const b = state.board;
  document.title = `${b.title} | Kanban`;
  document.body.className = `board-page bg-${b.color}`;

  const title = el('h1', { class: 'board-title', title: 'Rename board', tabindex: 0 }, b.title);
  const rename = () => inlineEdit(title, {
    value: state.board.title,
    onSave: (v) => patchBoard({ title: v }),
  });
  title.onclick = rename;
  title.onkeydown = (e) => { if (e.key === 'Enter') rename(); };

  const star = el('button', {
    class: 'hdr-btn' + (b.starred ? ' starred' : ''), title: b.starred ? 'Unstar' : 'Star',
    'aria-pressed': String(b.starred),
    onclick: async () => {
      try {
        await api(b.starred ? 'DELETE' : 'PUT', `/boards/${boardId}/star`);
        state.board.starred = !b.starred;
        renderHeader();
      } catch (err) {
        toast(err.message);
      }
    },
  }, icon(b.starred ? 'starFilled' : 'star'));

  const bgBtn = el('button', { class: 'hdr-btn', 'data-pop-anchor': '', title: 'Change background' },
    icon('image'), el('span', { class: 'hdr-label' }, 'Background'));
  bgBtn.onclick = () => popover(bgBtn, 'Background', (close) =>
    el('div', { class: 'swatches swatches-lg' }, BOARD_COLORS.map((c) =>
      el('button', {
        class: `swatch bg-${c}` + (c === state.board.color ? ' on' : ''), title: c,
        onclick: () => { close(); patchBoard({ color: c }); },
      }))));

  const actBtn = el('button', {
    class: 'hdr-btn' + ($activity.hidden ? '' : ' on'), title: 'Show activity',
    onclick: () => toggleActivity(),
  }, icon('activity'), el('span', { class: 'hdr-label' }, 'Activity'));

  const moreBtn = el('button', { class: 'hdr-btn', 'data-pop-anchor': '', title: 'Board actions' }, icon('dots'));
  moreBtn.onclick = () => popover(moreBtn, 'Board', (close) => el('div', { class: 'menu' },
    el('button', {
      class: 'menu-item',
      onclick: () => {
        close();
        if (confirm(`Archive "${state.board.title}"? You can reopen it from this page.`)) patchBoard({ archived: true });
      },
    }, icon('archive'), 'Archive this board')));

  const dot = el('span', {
    class: 'live-dot' + (live ? ' on' : ''),
    title: live ? 'Live: changes appear as they happen' : 'Reconnecting…',
  });

  $header.replaceChildren(title, star, el('span', { class: 'spacer' }), dot, bgBtn, actBtn, moreBtn);

  $banner.hidden = !b.archived_at;
  $banner.replaceChildren(
    'This board is archived.',
    el('button', { class: 'btn', onclick: () => patchBoard({ archived: false }) }, 'Reopen board'));
}

async function patchBoard(patch) {
  try {
    const b = await api('PATCH', `/boards/${boardId}`, patch);
    state.board = { ...b, starred: state.board.starred };
    renderHeader();
  } catch (err) {
    toast(err.message);
  }
}

function buildList(l) {
  const title = el('h2', { class: 'list-title', title: 'Rename list' }, l.title);
  title.onclick = () => inlineEdit(title, {
    value: state.lists.get(l.id).title,
    onSave: async (v) => upsertList(await api('PATCH', `/lists/${l.id}`, { title: v })),
  });
  const menu = el('button', { class: 'icon-btn', title: 'List actions', 'data-pop-anchor': '' }, icon('dots'));
  menu.onclick = () => popover(menu, 'List actions', (close) => el('div', { class: 'menu' },
    el('button', {
      class: 'menu-item',
      onclick: () => {
        close();
        listEls.get(l.id)?.root.querySelector('.add-card')?.click();
      },
    }, icon('plus'), 'Add card'),
    el('button', {
      class: 'menu-item',
      onclick: async () => {
        close();
        try {
          upsertList(await api('PATCH', `/lists/${l.id}`, { archived: true }));
        } catch (err) {
          toast(err.message);
        }
      },
    }, icon('archive'), 'Archive this list')));

  const cards = el('div', { class: 'cards', dataset: { listId: l.id } });
  const root = el('section', { class: 'list', dataset: { id: l.id } },
    el('header', { class: 'list-header' }, title, menu),
    cards,
    cardComposer(l.id, cards));

  Sortable.create(cards, {
    group: 'cards',
    animation: 150,
    delay: 150,
    delayOnTouchOnly: true,
    ghostClass: 'card-ghost',
    chosenClass: 'card-chosen',
    dragClass: 'card-drag',
    onStart: () => { dragging = true; },
    onEnd: onCardDrop,
  });
  listEls.set(l.id, { root, cards, title });
  renderCards(l.id);
  return root;
}

function renderCards(listId) {
  const entry = listEls.get(listId);
  if (!entry) return;
  if (busy()) {
    dirtyLists.add(listId);
    return;
  }
  const cards = [...state.cards.values()].filter((c) => c.list_id === listId).sort(byRank);
  entry.cards.replaceChildren(...cards.map(cardEl));
}

function renderAllCards() {
  for (const id of listEls.keys()) renderCards(id);
}

function placeLists() {
  if (busy()) {
    listsOrderDirty = true;
    return;
  }
  for (const l of [...state.lists.values()].sort(byRank)) {
    const entry = listEls.get(l.id);
    if (entry) addListCol.before(entry.root);
  }
}

function flushDeferred() {
  if (busy()) return;
  const ids = [...dirtyLists];
  dirtyLists.clear();
  ids.forEach(renderCards);
  if (listsOrderDirty) {
    listsOrderDirty = false;
    placeLists();
  }
}

function cardEl(c) {
  const labels = c.label_ids.map((id) => state.labels.get(id)).filter(Boolean)
    .sort((a, b) => a.id - b.id);
  const badges = [];
  if (c.due != null) {
    badges.push(el('span', { class: `badge ${dueStatus(c)}`, title: c.due_done ? 'Complete' : 'Due date' },
      icon('clock'), fmtDate(c.due)));
  }
  if (c.has_description) badges.push(el('span', { class: 'badge', title: 'This card has a description' }, icon('desc')));
  if (c.comments) badges.push(el('span', { class: 'badge', title: 'Comments' }, icon('comment'), String(c.comments)));
  if (c.check_total) {
    badges.push(el('span', {
      class: 'badge' + (c.check_done === c.check_total ? ' badge-complete' : ''), title: 'Checklist items',
    }, icon('check'), `${c.check_done}/${c.check_total}`));
  }
  return el('article', {
    class: 'card', dataset: { id: c.id }, tabindex: 0,
    onclick: () => openCard(c.id),
    onkeydown: (e) => { if (e.key === 'Enter') openCard(c.id); },
  },
  labels.length ? el('div', { class: 'card-labels' }, labels.map((lb) =>
    el('span', { class: `label-chip lc-${lb.color}`, title: lb.name || lb.color }, lb.name))) : null,
  el('div', { class: 'card-title' }, c.title),
  badges.length ? el('div', { class: 'badges' }, badges) : null);
}

// neighbors returns the move request body for an element just dropped among
// its siblings: the item above it, or failing that the item below it.
function neighbors(item) {
  const prev = item.previousElementSibling;
  const next = item.nextElementSibling;
  if (prev?.dataset.id) return { before_id: Number(prev.dataset.id) };
  if (next?.dataset.id) return { after_id: Number(next.dataset.id) };
  return {};
}

async function onCardDrop(evt) {
  dragging = false;
  const fromList = Number(evt.from.dataset.listId);
  const toList = Number(evt.to.dataset.listId);
  if (evt.from === evt.to && evt.oldIndex === evt.newIndex) {
    flushDeferred();
    return;
  }
  const id = Number(evt.item.dataset.id);
  pendingMoves++;
  try {
    const card = await api('POST', `/cards/${id}/move`, { list_id: toList, ...neighbors(evt.item) });
    const prev = state.cards.get(id);
    if (!prev || prev.version <= card.version) state.cards.set(id, summary(card));
  } catch (err) {
    toast(`Couldn't move the card: ${err.message}`);
  } finally {
    pendingMoves--;
    dirtyLists.add(fromList);
    dirtyLists.add(toList);
    flushDeferred();
  }
}

async function onListDrop(evt) {
  dragging = false;
  if (evt.oldIndex === evt.newIndex) {
    flushDeferred();
    return;
  }
  const id = Number(evt.item.dataset.id);
  pendingMoves++;
  try {
    const list = await api('POST', `/lists/${id}/move`, neighbors(evt.item));
    const prev = state.lists.get(id);
    if (!prev || prev.version <= list.version) state.lists.set(id, list);
  } catch (err) {
    toast(`Couldn't move the list: ${err.message}`);
  } finally {
    pendingMoves--;
    listsOrderDirty = true;
    flushDeferred();
  }
}

function cardComposer(listId, cardsEl) {
  const open = el('button', { class: 'add-card' }, icon('plus'), 'Add a card');
  const input = el('textarea', {
    class: 'composer-input', rows: 2, maxlength: 512, placeholder: 'Enter a title for this card…',
    'aria-label': 'Card title',
  });
  const form = el('form', { class: 'composer', hidden: true },
    input,
    el('div', { class: 'composer-actions' },
      el('button', { class: 'btn primary', type: 'submit' }, 'Add card'),
      el('button', { class: 'icon-btn', type: 'button', title: 'Cancel', onclick: () => close() }, icon('close'))));
  const close = () => {
    form.hidden = true;
    open.hidden = false;
    input.value = '';
  };
  open.onclick = () => {
    open.hidden = true;
    form.hidden = false;
    input.focus();
  };
  input.onkeydown = (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      form.requestSubmit();
    } else if (e.key === 'Escape') {
      close();
    }
  };
  input.onblur = () => setTimeout(() => {
    if (!form.contains(document.activeElement) && !input.value.trim()) close();
  }, 150);
  form.onsubmit = async (e) => {
    e.preventDefault();
    const title = input.value.trim();
    if (!title) return input.focus();
    input.value = '';
    input.focus();
    try {
      upsertCard(await api('POST', `/lists/${listId}/cards`, { title }));
      cardsEl.scrollTop = cardsEl.scrollHeight;
    } catch (err) {
      input.value = title;
      toast(err.message);
    }
  };
  return el('div', { class: 'list-footer' }, open, form);
}

function buildAddList() {
  const open = el('button', { class: 'add-list' }, icon('plus'), 'Add another list');
  const input = el('input', { class: 'composer-input', maxlength: 200, placeholder: 'Enter list title…', 'aria-label': 'List title' });
  const form = el('form', { class: 'composer', hidden: true },
    input,
    el('div', { class: 'composer-actions' },
      el('button', { class: 'btn primary', type: 'submit' }, 'Add list'),
      el('button', { class: 'icon-btn', type: 'button', title: 'Cancel', onclick: () => close() }, icon('close'))));
  const close = () => {
    form.hidden = true;
    open.hidden = false;
    input.value = '';
  };
  open.onclick = () => {
    open.hidden = true;
    form.hidden = false;
    input.focus();
  };
  input.onkeydown = (e) => { if (e.key === 'Escape') close(); };
  form.onsubmit = async (e) => {
    e.preventDefault();
    const title = input.value.trim();
    if (!title) return input.focus();
    input.value = '';
    try {
      upsertList(await api('POST', `/boards/${boardId}/lists`, { title }));
      $lists.scrollLeft = $lists.scrollWidth;
    } catch (err) {
      input.value = title;
      toast(err.message);
    }
    input.focus();
  };
  return el('div', { class: 'add-list-col' }, open, form);
}

// ---- activity ----

let activityLoaded = false;
let oldestSeq = 0;
const seenSeqs = new Set();

function activityItem(ev) {
  return el('li', { class: 'act-item' },
    el('span', { class: 'avatar', title: ev.user_name }, initials(ev.user_name)),
    el('div', {},
      el('div', { class: 'act-text' }, el('strong', {}, ev.user_name), ' ', describe(ev)),
      ev.type === 'comment.created' && ev.data.excerpt ? el('div', { class: 'act-quote' }, ev.data.excerpt) : null,
      el('time', { class: 'muted', datetime: new Date(ev.created).toISOString() }, relTime(ev.created))));
}

export function initials(name) {
  return name.split(/\s+/).filter(Boolean).slice(0, 2).map((w) => w[0].toUpperCase()).join('');
}

async function toggleActivity() {
  $activity.hidden = !$activity.hidden;
  renderHeader();
  if ($activity.hidden || activityLoaded) return;
  activityLoaded = true;
  const listEl = el('ol', { class: 'act-list' });
  const more = el('button', { class: 'btn wide', hidden: true }, 'Load more');
  $activity.replaceChildren(el('h3', {}, icon('activity'), 'Activity'), listEl, more);
  const page = async () => {
    try {
      const evs = await api('GET', `/boards/${boardId}/activity?limit=50${oldestSeq ? `&before=${oldestSeq}` : ''}`);
      for (const ev of evs) {
        if (seenSeqs.has(ev.seq)) continue;
        seenSeqs.add(ev.seq);
        listEl.append(activityItem(ev));
        oldestSeq = ev.seq;
      }
      more.hidden = evs.length < 50;
    } catch (err) {
      toast(err.message);
    }
  };
  more.onclick = page;
  await page();
}

function addActivity(ev) {
  if (!activityLoaded || seenSeqs.has(ev.seq)) return;
  seenSeqs.add(ev.seq);
  $activity.querySelector('.act-list')?.prepend(activityItem(ev));
}

// ---- live updates ----

function setLive(on) {
  if (live === on) return;
  live = on;
  const dot = $header.querySelector('.live-dot');
  if (dot) {
    dot.classList.toggle('on', on);
    dot.title = on ? 'Live: changes appear as they happen' : 'Reconnecting…';
  }
}

function applyEvent(ev) {
  if (ev.seq <= state.seq) return;
  state.seq = ev.seq;
  addActivity(ev);
  if (ev.client_id === CLIENT_ID) return; // this tab already applied it
  const d = ev.data || {};
  if (ev.type === 'list.restored') {
    reload(); // its cards aren't in the model
    return;
  }
  if (d.board) {
    state.board = { ...d.board, starred: state.board.starred };
    renderHeader();
  }
  if (d.list) upsertList(d.list);
  if (d.label && ev.type.startsWith('label.')) upsertLabel(d.label);
  if (ev.type === 'label.deleted') removeLabel(d.label_id);
  if (d.card) {
    upsertCard(d.card);
    if (currentCardId() === d.card.id) refreshCard();
  }
}

function connect() {
  events?.close();
  events = new EventSource(`/api/boards/${boardId}/events?after=${state.seq}`);
  events.onopen = () => setLive(true);
  events.onmessage = (m) => {
    try {
      applyEvent(JSON.parse(m.data));
    } catch (err) {
      console.error('bad event', err);
    }
  };
  events.addEventListener('reset', () => reload());
  events.onerror = () => {
    setLive(false);
    if (events.readyState === EventSource.CLOSED) setTimeout(reload, 3000);
  };
}

async function reload() {
  try {
    applySnapshot(await api('GET', `/boards/${boardId}`));
    closePopover();
    renderAll();
    connect();
    if (currentCardId()) refreshCard();
  } catch (err) {
    toast(err.message);
    setTimeout(reload, 5000);
  }
}

// ---- start ----

function routeHash() {
  const m = location.hash.match(/^#card-(\d+)$/);
  if (m) openCard(Number(m[1]));
  else closeCard();
}

async function start() {
  if (!boardId) {
    location.href = './';
    return;
  }
  try {
    applySnapshot(await api('GET', `/boards/${boardId}`));
  } catch (err) {
    $lists.replaceChildren(el('div', { class: 'empty' },
      el('h2', {}, err.status === 404 ? 'Board not found' : "Couldn't load this board"),
      el('p', {}, err.status === 404 ? 'It may have been deleted, or the link is wrong.' : err.message),
      el('a', { class: 'btn primary', href: './' }, 'Back to boards')));
    return;
  }
  setupCardModal({ state, upsertCard, upsertLabel, removeLabel });
  renderAll();
  Sortable.create($lists, {
    animation: 150,
    draggable: '.list',
    handle: '.list-header',
    filter: '.inline-input, button',
    preventOnFilter: false,
    ghostClass: 'list-ghost',
    onStart: () => { dragging = true; },
    onEnd: onListDrop,
  });
  connect();
  window.addEventListener('hashchange', routeHash);
  routeHash();
}

start();
