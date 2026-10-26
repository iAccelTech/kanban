// The board list page.
import { api } from './api.js';
import { el, icon, toast, popover, BOARD_COLORS } from './dom.js';

const root = document.getElementById('boards');

async function load() {
  try {
    render(await api('GET', '/boards'));
  } catch (err) {
    root.replaceChildren(el('p', { class: 'error' }, err.message));
  }
}

function tile(b) {
  const star = el('button', {
    class: 'tile-star' + (b.starred ? ' on' : ''),
    title: b.starred ? 'Unstar this board' : 'Star this board',
    'aria-pressed': String(b.starred),
    onclick: async (e) => {
      e.preventDefault();
      try {
        await api(b.starred ? 'DELETE' : 'PUT', `/boards/${b.id}/star`);
        load();
      } catch (err) {
        toast(err.message);
      }
    },
  }, icon(b.starred ? 'starFilled' : 'star'));
  return el('a', { class: `tile bg-${b.color}`, href: `board.html?id=${b.id}` },
    el('span', { class: 'tile-title' }, b.title), star);
}

function section(title, iconName, tiles) {
  return el('section', { class: 'board-section' },
    el('h2', {}, icon(iconName), title),
    el('div', { class: 'tiles' }, tiles));
}

function newTile() {
  const btn = el('button', { class: 'tile tile-new', 'data-pop-anchor': '' }, 'Create new board');
  btn.onclick = () => popover(btn, 'Create board', (close) => {
    let color = 'blue';
    const preview = el('div', { class: `board-preview bg-${color}` }, el('div'), el('div'), el('div'));
    const swatches = el('div', { class: 'swatches' }, BOARD_COLORS.map((c) =>
      el('button', {
        type: 'button', class: `swatch bg-${c}` + (c === color ? ' on' : ''), title: c,
        onclick: (e) => {
          color = c;
          preview.className = `board-preview bg-${c}`;
          swatches.querySelectorAll('.swatch').forEach((s) => s.classList.remove('on'));
          e.currentTarget.classList.add('on');
        },
      })));
    const title = el('input', { name: 'title', required: true, maxlength: 200, autocomplete: 'off' });
    const submit = el('button', { class: 'btn primary wide', type: 'submit' }, 'Create');
    return el('form', {
      class: 'stack',
      onsubmit: async (e) => {
        e.preventDefault();
        submit.disabled = true;
        try {
          const b = await api('POST', '/boards', { title: title.value, color });
          location.href = `board.html?id=${b.id}`;
        } catch (err) {
          toast(err.message);
          submit.disabled = false;
        }
      },
    }, preview,
    el('label', { class: 'field-label' }, 'Background'), swatches,
    el('label', { class: 'field-label' }, 'Board title', title),
    submit);
  });
  return btn;
}

function render(boards) {
  const starred = boards.filter((b) => b.starred);
  root.replaceChildren(
    starred.length ? section('Starred boards', 'starFilled', starred.map(tile)) : null,
    section('Your boards', 'board', [...boards.map(tile), newTile()]),
  );
}

load();
