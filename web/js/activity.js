// Turns board events into sentences for the activity feed.
import { el, fmtDate } from './dom.js';

const b = (text) => el('strong', {}, text);
const cardLink = (card) => el('a', { href: `#card-${card.id}` }, card.title);

function labelName(label) {
  return label.name || `${label.color} label`;
}

// describe returns the nodes after the user's name, e.g. "added Foo to To do".
export function describe(ev) {
  const d = ev.data || {};
  const card = d.card ? cardLink(d.card) : null;
  switch (ev.type) {
    case 'board.created': return ['created this board'];
    case 'board.renamed': return ['renamed this board to ', b(d.board.title)];
    case 'board.recolored': return ['changed the background of this board'];
    case 'board.archived': return ['archived this board'];
    case 'board.reopened': return ['reopened this board'];
    case 'list.created': return ['added list ', b(d.list.title)];
    case 'list.renamed': return ['renamed list ', b(d.from), ' to ', b(d.list.title)];
    case 'list.moved': return ['moved list ', b(d.list.title)];
    case 'list.archived': return ['archived list ', b(d.list.title)];
    case 'list.restored': return ['restored list ', b(d.list.title)];
    case 'card.created': return ['added ', card, ' to ', d.list_title];
    case 'card.renamed': return ['renamed ', b(d.from), ' to ', card];
    case 'card.described': return ['updated the description of ', card];
    case 'card.due':
      return d.card.due ? ['set ', card, ' to be due ', fmtDate(d.card.due, true)] : ['removed the due date from ', card];
    case 'card.due_done':
      return d.card.due_done ? ['marked the due date on ', card, ' complete'] : ['marked the due date on ', card, ' incomplete'];
    case 'card.moved':
      return d.from_list_title ? ['moved ', card, ' from ', d.from_list_title, ' to ', d.to_list_title] : ['moved ', card];
    case 'card.archived': return ['archived ', card];
    case 'card.restored': return ['restored ', card];
    case 'card.labeled':
      return d.on ? ['added ', b(labelName(d.label)), ' to ', card] : ['removed ', b(labelName(d.label)), ' from ', card];
    case 'checklist.created': return ['added checklist ', b(d.title), ' to ', card];
    case 'checklist.deleted': return ['removed checklist ', b(d.title), ' from ', card];
    case 'checkitem.created': return ['added ', b(d.text), ' to a checklist on ', card];
    case 'checkitem.completed': return ['completed ', b(d.text), ' on ', card];
    case 'checkitem.uncompleted': return ['marked ', b(d.text), ' incomplete on ', card];
    case 'checkitem.deleted': return ['removed a checklist item from ', card];
    case 'comment.created': return ['commented on ', card];
    case 'comment.edited': return ['edited a comment on ', card];
    case 'comment.deleted': return ['deleted a comment on ', card];
    case 'label.created': return ['created label ', b(labelName(d.label))];
    case 'label.updated': return ['updated label ', b(labelName(d.label))];
    case 'label.deleted': return ['deleted a label'];
    default: return [ev.type];
  }
}
