// Простые интерактивные подсказки поверх readline (без внешних зависимостей).
import readline from 'node:readline';
import readlinePromises from 'node:readline/promises';
import { stdin, stdout } from 'node:process';

let rl = null;

function iface() {
  rl ??= readlinePromises.createInterface({ input: stdin, output: stdout });
  return rl;
}

export function closeUi() {
  rl?.close();
  rl = null;
}

export const isInteractive = () => stdin.isTTY && stdout.isTTY;

export async function ask(question, fallback = '') {
  const answer = (await iface().question(question)).trim();
  return answer || fallback;
}

export async function confirm(question, def = true) {
  const hint = def ? 'Д/н' : 'д/Н';
  const answer = (await ask(`${question} [${hint}] `)).toLowerCase();
  if (!answer) return def;
  return ['д', 'da', 'да', 'y', 'yes'].includes(answer);
}

const ESC = '\x1b[';
const DIM = `${ESC}2m`;
const CYAN = `${ESC}36m`;
const BOLD = `${ESC}1m`;
const RESET = `${ESC}0m`;

/**
 * Собирает строку пункта: сначала обрезка по ширине терминала (перенос строки
 * сломал бы перерисовку), только потом раскраска — иначе счёт символов уедет.
 */
function itemLine(item, i, active, width) {
  const prefix = active ? ` ❯ ${String(i + 1).padStart(2)}. ` : `   ${String(i + 1).padStart(2)}. `;
  const hint = item.hint ? `  ${item.hint}` : '';
  let chars = [...`${prefix}${item.label}${hint}`];
  if (chars.length > width) chars = [...chars.slice(0, Math.max(1, width - 1)), '…'];

  const head = chars.slice(0, prefix.length + [...item.label].length).join('');
  const tail = chars.slice(prefix.length + [...item.label].length).join('');
  const body = active ? `${CYAN}${BOLD}${head}${RESET}` : head;
  return tail ? `${body}${DIM}${tail}${RESET}` : body;
}

function drawFrame(title, items, index, offset, visible) {
  const width = (stdout.columns || 80) - 1;
  const lines = [title];
  if (offset > 0) lines.push(`${DIM}   ↑ ещё ${offset}${RESET}`);

  for (let i = offset; i < Math.min(items.length, offset + visible); i++) {
    lines.push(itemLine(items[i], i, i === index, width));
  }

  const rest = items.length - (offset + visible);
  if (rest > 0) lines.push(`${DIM}   ↓ ещё ${rest}${RESET}`);
  lines.push(`${DIM}   ↑/↓ — выбор, Enter — подтвердить, q — выход${RESET}`);
  return lines;
}

/** Выбор стрелками. Используется, когда есть настоящий терминал. */
function selectRaw(title, items, def) {
  return new Promise((resolve, reject) => {
    // readline мешает читать клавиши напрямую — закрываем, ask() создаст его заново.
    closeUi();

    let index = Math.min(Math.max(def, 0), items.length - 1);
    const visible = Math.max(3, Math.min(items.length, (stdout.rows || 24) - 6));
    let offset = Math.max(0, Math.min(index - Math.floor(visible / 2), items.length - visible));
    let printed = 0;
    let digits = '';

    const draw = () => {
      if (printed) stdout.write(`${ESC}${printed}A`);
      stdout.write(`${ESC}0J`);
      const lines = drawFrame(title, items, index, offset, visible);
      stdout.write(`${lines.join('\n')}\n`);
      printed = lines.length;
    };

    const move = (delta) => {
      index = (index + delta + items.length) % items.length;
      if (index < offset) offset = index;
      if (index >= offset + visible) offset = index - visible + 1;
      offset = Math.max(0, Math.min(offset, Math.max(0, items.length - visible)));
      draw();
    };

    const finish = (fn, arg) => {
      stdin.off('keypress', onKey);
      if (stdin.isTTY) stdin.setRawMode(false);
      stdin.pause();
      stdout.write(`${ESC}?25h`); // вернуть курсор
      fn(arg);
    };

    const onKey = (str, key = {}) => {
      if (key.ctrl && key.name === 'c') {
        finish(reject, new Error('прервано пользователем'));
        return;
      }
      switch (key.name) {
        case 'up': case 'k': move(-1); return;
        case 'down': case 'j': move(1); return;
        case 'pageup': move(-visible); return;
        case 'pagedown': move(visible); return;
        case 'home': move(-index); return;
        case 'end': move(items.length - 1 - index); return;
        case 'return': case 'enter': case 'space':
          finish(resolve, index);
          return;
        case 'escape': case 'q':
          finish(reject, new Error('выбор отменён'));
          return;
        default: break;
      }
      // Номер пункта можно набрать цифрами: «12» + Enter или просто «12».
      if (str && /^\d$/.test(str)) {
        digits += str;
        const n = Number(digits);
        if (n >= 1 && n <= items.length) move(n - 1 - index);
        // Двузначные номера набираются не мгновенно — сбрасываем накопленное с паузой.
        clearTimeout(onKey.timer);
        onKey.timer = setTimeout(() => { digits = ''; }, 700);
        if (n * 10 > items.length) digits = '';
      }
    };

    readline.emitKeypressEvents(stdin);
    if (stdin.isTTY) stdin.setRawMode(true);
    stdin.resume();
    stdout.write(`${ESC}?25l`); // спрятать курсор
    stdin.on('keypress', onKey);
    draw();
  });
}

/** Запасной вариант без терминала: номер пункта с клавиатуры. */
async function selectNumeric(title, items, def) {
  console.log(`\n${title}`);
  items.forEach((it, i) => {
    const mark = i === def ? '›' : ' ';
    console.log(`  ${mark} ${String(i + 1).padStart(2)}. ${it.label}${it.hint ? `  ${it.hint}` : ''}`);
  });
  for (;;) {
    const raw = await ask(`Номер [${def + 1}]: `, String(def + 1));
    const n = Number(raw);
    if (Number.isInteger(n) && n >= 1 && n <= items.length) return n - 1;
    console.log('Нужен номер из списка.');
  }
}

/**
 * Выбор одного пункта из списка.
 * @param {string} title
 * @param {Array<{label: string, hint?: string}>} items
 * @param {{def?: number}} [opts]
 * @returns {Promise<number>} индекс выбранного
 */
export async function select(title, items, opts = {}) {
  const def = opts.def ?? 0;
  if (!items.length) throw new Error('пустой список выбора');
  if (items.length === 1) return 0;
  if (!isInteractive()) return selectNumeric(`\n${title}`, items, def);
  return selectRaw(`\n${title}`, items, def);
}
