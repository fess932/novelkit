#!/usr/bin/env node
// CLI: скачивает главы ranobelib.me с выбором команды перевода и собирает EPUB.
import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';
import { Client } from '../src/http.js';
import { Api, SITE, parseSlug, collectBranches, chaptersOfBranch } from '../src/api.js';
import { downloadChapters, buildFromCache } from '../src/download.js';
import { plainText } from '../src/render.js';
import {
  STATE_VERSION, ensureJob, jobDirFor, listJobs, loadState, saveState, progressOf,
} from '../src/state.js';
import { ask, confirm, select, closeUi, isInteractive } from '../src/ui.js';
import { findMagick } from '../src/images.js';

const HELP = `
rlib — скачивание ранобэ с ranobelib.me в EPUB

Использование:
  rlib                       меню: выбрать действие и книгу из кэша
  rlib <ссылка|slug|поисковый запрос> [опции]
  rlib --resume [каталог задания]
  rlib --list-jobs

Опции:
  --branch <id>       id ветки перевода (по умолчанию — выбор из списка)
  --branch-name <ст>  выбрать ветку по названию команды (подстрока)
  --list-branches     показать ветки перевода и выйти
  --from <n>          с какой главы по счёту (1 — первая)
  --to <n>            по какую главу включительно
  --out <файл>        путь к .epub (по умолчанию — рядом, по названию книги)
  --work-dir <кат>    каталог кэша заданий (по умолчанию .rlib)
  --delay <мс>        базовая пауза между запросами (по умолчанию 1500)
  --jitter <мс>       случайная добавка к паузе (по умолчанию 700)
  --retries <n>       повторов при сетевой ошибке/429 (по умолчанию 4)
  --no-images         не скачивать иллюстрации
  --build-only        собрать EPUB из уже скачанного кэша
  --refresh-meta      принудительно обновить описание/автора/жанры (битые чинятся сами)
  --compress          сжать иллюстрации при сборке (оригиналы в кэше не трогаются)
  --max-image <px>    большая сторона картинки при --compress (по умолчанию 1200)
  --quality <1-100>   качество jpeg при --compress (по умолчанию 82)
  --yes               без вопросов (берётся ветка с наибольшим числом глав)
  --help

Загрузка останавливается на первой ошибке; продолжить — rlib --resume
`;

function parseArgs(argv) {
  const opts = {
    _: [],
    delay: 1500,
    jitter: 700,
    retries: 4,
    images: true,
    workDir: '.rlib',
  };
  const numeric = new Set(['delay', 'jitter', 'retries', 'from', 'to', 'branch', 'max-image', 'quality']);
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (!a.startsWith('--')) {
      opts._.push(a);
      continue;
    }
    const key = a.replace(/^--/, '');
    switch (key) {
      case 'help': opts.help = true; break;
      case 'no-images': opts.images = false; break;
      case 'list-branches': opts.listBranches = true; break;
      case 'list-jobs': opts.listJobs = true; break;
      case 'build-only': opts.buildOnly = true; break;
      case 'compress': opts.compress = true; break;
      case 'refresh-meta': opts.refreshMeta = true; break;
      case 'yes': opts.yes = true; break;
      case 'resume': {
        const next = argv[i + 1];
        opts.resume = next && !next.startsWith('--') ? (i++, next) : true;
        break;
      }
      case 'branch-name': opts.branchName = argv[++i]; break;
      case 'work-dir': opts.workDir = argv[++i]; break;
      case 'out': opts.out = argv[++i]; break;
      default: {
        if (!numeric.has(key)) throw new Error(`неизвестная опция --${key}`);
        const value = Number(argv[++i]);
        if (!Number.isFinite(value)) throw new Error(`--${key} ждёт число`);
        opts[key] = value;
      }
    }
  }
  return opts;
}

const log = (msg = '') => console.log(msg);

function safeFileName(str) {
  return String(str)
    .replace(/[/\\?%*:|"<>]/g, '')
    .replace(/\s+/g, ' ')
    .trim()
    .slice(0, 120);
}

/** Метаданные книги из карточки сайта. Ветка перевода добавляется отдельно. */
function metaFromManga(manga, slug) {
  const title = manga.rus_name || manga.eng_name || manga.name;
  return {
    title,
    origTitle: manga.eng_name && manga.eng_name !== title ? manga.eng_name : manga.name,
    authors: (manga.authors ?? []).map((a) => a.rus_name || a.name).filter(Boolean),
    publisher: (manga.publisher ?? [])[0]?.name || null,
    genres: (manga.genres ?? []).map((g) => g.name),
    year: manga.releaseDate || null,
    summary: plainText(manga.summary),
    sourceUrl: `${SITE}/ru/book/${slug}`,
  };
}

async function resolveSlug(api, input, opts) {
  const direct = parseSlug(input);
  // Слаг сайта всегда начинается с числового id: "26690--omniscient-...".
  if (direct && /^\d+--/.test(direct)) return direct;

  const query = input;
  log(`Ищу «${query}»…`);
  const found = await api.search(query);
  if (!found.length) throw new Error(`по запросу «${query}» ничего не нашлось`);

  if (!isInteractive() || opts.yes) {
    log(`Выбрано: ${found[0].rus_name || found[0].name}`);
    return found[0].slug_url;
  }
  const idx = await select(
    'Что скачиваем?',
    found.slice(0, 15).map((m) => ({
      label: m.rus_name || m.name,
      hint: `(${m.eng_name || m.name})`,
    })),
  );
  return found[idx].slug_url;
}

async function chooseBranch(branches, opts) {
  // Пустые ветки сайт показывает вкладкой, но качать в них нечего.
  const usable = branches.filter((b) => b.count > 0);
  const empty = branches.length - usable.length;
  if (!usable.length) throw new Error('ни в одной ветке перевода нет глав');

  if (opts.branch != null) {
    const found = branches.find((b) => String(b.id) === String(opts.branch));
    if (!found) throw new Error(`ветка ${opts.branch} у этой книги не найдена`);
    if (!found.count) throw new Error(`в ветке ${opts.branch} (${found.label}) нет глав`);
    return found;
  }
  if (opts.branchName) {
    const needle = opts.branchName.toLowerCase();
    const matches = branches.filter(
      (b) => b.label.toLowerCase().includes(needle)
        || b.name?.toLowerCase().includes(needle)
        || b.teams.some((t) => t.toLowerCase().includes(needle)),
    );
    const found = matches.find((b) => b.count > 0);
    if (!found) {
      throw new Error(
        matches.length
          ? `в ветке «${matches[0].label}» нет глав`
          : `ветка со словом «${opts.branchName}» не найдена`,
      );
    }
    return found;
  }
  if (usable.length === 1) return usable[0];
  if (!isInteractive() || opts.yes) {
    log(`Веток перевода: ${usable.length}; беру самую полную — ${usable[0].label}`);
    return usable[0];
  }
  if (empty) log(`\n(ещё ${empty} вкладк${empty === 1 ? 'а' : 'и'} на сайте без глав — пропускаю)`);
  const idx = await select(
    'Чей перевод скачиваем?',
    usable.map((b) => ({
      label: b.label,
      hint: `— ${b.count} гл.${b.uploaders.length ? `, залил ${b.uploaders.slice(0, 2).join(', ')}` : ''}`,
    })),
  );
  return usable[idx];
}

async function chooseRange(list, opts) {
  let from = opts.from ?? 1;
  let to = opts.to ?? list.length;

  if (opts.from == null && opts.to == null && isInteractive() && !opts.yes) {
    const answer = await ask(`Диапазон глав 1–${list.length} (Enter — все, например 1-100): `);
    if (answer) {
      const m = answer.match(/^(\d+)\s*(?:-|–|\.\.)\s*(\d+)$/) || answer.match(/^(\d+)$/);
      if (!m) throw new Error(`не понял диапазон «${answer}»`);
      from = Number(m[1]);
      to = m[2] ? Number(m[2]) : list.length;
    }
  }
  from = Math.max(1, from);
  to = Math.min(list.length, to);
  if (from > to) throw new Error(`пустой диапазон: ${from}–${to}`);
  return list.slice(from - 1, to);
}

function printProgress({ done, total, title, eta }) {
  const pct = String(Math.round((done / total) * 100)).padStart(3);
  log(`  [${pct}%] ${done}/${total}  ${title}${eta !== '—' ? `  ~${eta}` : ''}`);
}

async function fetchCover(api, manga, dir, state) {
  const url = manga.cover?.default || manga.cover?.md || manga.cover?.thumbnail;
  if (!url) return;
  try {
    const { buffer } = await api.binary(url);
    fs.writeFileSync(path.join(dir, 'cover.bin'), buffer);
    state.coverExt = (url.match(/\.([a-z0-9]{2,5})(?:\?|$)/i)?.[1] || 'jpg').toLowerCase();
  } catch (err) {
    log(`  ! обложка не скачалась: ${err.message}`);
  }
}

async function createJob(api, opts) {
  const input = opts._.join(' ').trim();
  if (!input) throw new Error('нужна ссылка, slug или название книги');

  const slug = await resolveSlug(api, input, opts);
  log('Читаю карточку книги…');
  const manga = await api.manga(slug);
  const allChapters = await api.chapters(slug);
  if (!allChapters.length) throw new Error('у книги нет глав');

  const branchMeta = await api.branches(manga.id);
  const branches = collectBranches(allChapters, branchMeta);
  if (opts.listBranches) {
    log(`\n${manga.rus_name || manga.name} — веток перевода: ${branches.length}`);
    for (const b of branches) {
      const who = b.uploaders.length ? ` [${b.uploaders.slice(0, 3).join(', ')}]` : '';
      const own = b.name && b.name !== b.label ? ` («${b.name}»)` : '';
      log(`  id=${b.id ?? '(нет)'}  ${b.count ? `${b.count} гл.` : 'нет глав'}  ${b.label}${own}${who}`);
    }
    return null;
  }

  const branch = await chooseBranch(branches, opts);
  const list = chaptersOfBranch(allChapters, branch.id);
  log(`Перевод: ${branch.label} — ${list.length} гл.`);
  const picked = await chooseRange(list, opts);

  const title = manga.rus_name || manga.eng_name || manga.name;
  const outDefault = `${safeFileName(title)}${branches.length > 1 ? ` [${safeFileName(branch.label)}]` : ''}.epub`;

  const dir = jobDirFor(opts.workDir, slug, branch.id);
  ensureJob(dir);

  const existing = loadState(dir);
  if (existing && existing.chapters.some((c) => c.done)) {
    const { done, total } = progressOf(existing);
    const reuse = opts.yes || !isInteractive()
      ? true
      : await confirm(`Найден кэш этой книги (${done}/${total} глав). Продолжить с него?`, true);
    if (reuse) {
      existing.out = opts.out || existing.out;
      // Карточка книги уже загружена — заодно обновляем метаданные.
      existing.meta = { ...existing.meta, ...metaFromManga(manga, slug) };
      // Диапазон мог измениться — добавляем недостающие главы, скачанное не трогаем.
      const known = new Map(existing.chapters.map((c) => [c.index, c]));
      existing.chapters = picked.map((c) => known.get(c.index) ?? { ...c, done: false });
      saveState(dir, existing);
      return { dir, state: existing };
    }
  }

  const state = {
    version: STATE_VERSION,
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
    slug,
    slugUrl: `${SITE}/ru/book/${slug}`,
    branchId: branch.id,
    branchLabel: branch.label,
    images: opts.images,
    out: opts.out || outDefault,
    coverExt: null,
    assets: {},
    warnings: [],
    meta: {
      ...metaFromManga(manga, slug),
      translators: [...new Set([...(branch.teams.length ? branch.teams : [branch.label]), ...branch.uploaders])],
    },
    chapters: picked.map((c) => ({ ...c, done: false })),
  };

  saveState(dir, state);
  await fetchCover(api, manga, dir, state);
  saveState(dir, state);
  return { dir, state };
}

async function pickJob(opts) {
  if (typeof opts.resume === 'string') {
    const dir = opts.resume;
    const state = loadState(dir);
    if (!state) throw new Error(`в ${dir} нет сохранённого задания`);
    return { dir, state };
  }
  const jobs = listJobs(opts.workDir);
  if (!jobs.length) throw new Error(`в ${opts.workDir} нет незавершённых заданий`);

  const unfinished = jobs.filter((j) => progressOf(j.state).left > 0);
  const pool = unfinished.length ? unfinished : jobs;
  if (pool.length === 1 || !isInteractive() || opts.yes) return pool[0];

  const idx = await select(
    'Какое задание продолжаем?',
    pool.map((j) => {
      const { done, total } = progressOf(j.state);
      return { label: j.state.meta.title, hint: `— ${done}/${total} гл., перевод: ${j.state.branchLabel}` };
    }),
  );
  return pool[idx];
}

/** Меню при запуске без аргументов: действие → книга из кэша → сжатие. */
async function mainMenu(opts) {
  const jobs = listJobs(opts.workDir);
  const unfinished = jobs.filter((j) => progressOf(j.state).left > 0);

  const actions = [{ key: 'new', label: 'Скачать новую книгу', hint: '— по ссылке или названию' }];
  if (unfinished.length) {
    actions.push({ key: 'resume', label: 'Продолжить загрузку', hint: `— незавершённых: ${unfinished.length}` });
  }
  if (jobs.length) {
    actions.push({ key: 'build', label: 'Собрать EPUB из кэша', hint: `— книг в кэше: ${jobs.length}` });
    actions.push({ key: 'list', label: 'Показать, что в кэше' });
  }

  const action = actions[await select('Что делаем?', actions)].key;

  if (action === 'list') {
    for (const j of jobs) {
      const { done, total } = progressOf(j.state);
      log(`\n${j.state.meta.title}\n    ${done}/${total} гл., перевод: ${j.state.branchLabel}\n    ${j.dir}`);
    }
    return false;
  }

  if (action === 'new') {
    const input = await ask('\nСсылка, slug или название книги: ');
    if (!input) {
      log('Пусто — выхожу.');
      return false;
    }
    opts._ = [input];
  } else {
    const pool = action === 'resume' ? unfinished : jobs;
    const idx = await select(
      action === 'resume' ? 'Какую догружаем?' : 'Какую собираем?',
      pool.map((j) => {
        const { done, total } = progressOf(j.state);
        return {
          label: j.state.meta.title,
          hint: `— ${done}/${total} гл., ${j.state.branchLabel}`,
        };
      }),
    );
    opts.resume = pool[idx].dir;
    opts.buildOnly = action === 'build';
  }

  // Сжатие есть смысл предлагать, только если ImageMagick установлен.
  if (findMagick()) {
    opts.compress = await confirm('\nСжать иллюстрации? (книга станет в разы легче)', true);
  }
  return true;
}

async function main() {
  const opts = parseArgs(process.argv.slice(2));
  if (opts.help) {
    log(HELP);
    return 0;
  }
  if (!opts._.length && !opts.resume && !opts.listJobs) {
    if (!isInteractive()) {
      log(HELP);
      return 0;
    }
    if (!(await mainMenu(opts))) return 0;
  }

  if (opts.listJobs) {
    const jobs = listJobs(opts.workDir);
    if (!jobs.length) log(`Заданий в ${opts.workDir} нет.`);
    for (const j of jobs) {
      const { done, total } = progressOf(j.state);
      log(`${j.dir}\n    ${j.state.meta.title} — ${done}/${total} гл., перевод: ${j.state.branchLabel}`);
    }
    return 0;
  }

  const client = new Client({
    delay: opts.delay,
    jitter: opts.jitter,
    retries: opts.retries,
    onNotice: (msg) => log(`  · ${msg}`),
  });
  const api = new Api(client);

  const job = opts.resume ? await pickJob(opts) : await createJob(api, opts);
  if (!job) return 0; // был --list-branches

  const { dir, state } = job;
  if (opts.out) {
    // Путь запоминаем сразу: иначе следующая сборка уйдёт по старому адресу.
    state.out = opts.out;
    saveState(dir, state);
  }
  if (opts.images === false) state.images = false;

  // Старые версии сохраняли описание как "[object Object]".
  // Чиним молча при любой сборке — это один запрос за карточкой книги.
  const brokenMeta = typeof state.meta.summary !== 'string'
    || /^\[object \w+\]$/.test(state.meta.summary.trim());
  if (opts.refreshMeta || brokenMeta) {
    try {
      const manga = await api.manga(state.slug);
      state.meta = { ...state.meta, ...metaFromManga(manga, state.slug) };
      saveState(dir, state);
      log(brokenMeta ? 'Описание в кэше было битым — обновил из карточки книги.' : 'Метаданные обновлены.');
    } catch (err) {
      // Нет сети — не повод не собрать книгу; описание просто не попадёт в неё.
      log(`  ! метаданные обновить не вышло: ${err.message}`);
    }
  }

  const { done, total } = progressOf(state);
  log(`\nКнига: ${state.meta.title}`);
  log(`Перевод: ${state.branchLabel}`);
  log(`Глав в задании: ${total}${done ? ` (уже скачано ${done})` : ''}`);
  log(`Кэш: ${dir}`);

  if (!opts.buildOnly) {
    log(`Пауза между запросами: ${opts.delay}–${opts.delay + opts.jitter} мс\n`);
    try {
      await downloadChapters({ dir, state, api, log, onProgress: printProgress });
    } catch (err) {
      saveState(dir, state);
      const left = progressOf(state).left;
      log(`\n✗ Загрузка остановлена: ${err.message}`);
      if (err.chapter) log(`  на главе: ${err.chapter.number} (том ${err.chapter.volume})`);
      log(`  скачано ${progressOf(state).done}/${total}, осталось ${left}`);
      log(`  продолжить: rlib --resume ${dir}`);
      log('  если ошибка из-за рейт-лимита, добавьте --delay 4000');
      return 1;
    }
  }

  log('\nСобираю EPUB…');
  const optimize = opts.compress
    ? { maxSize: opts['max-image'] ?? 1200, quality: opts.quality ?? 82 }
    : null;
  const res = buildFromCache({ dir, state, outFile: state.out, log, optimize });
  const mb = (res.size / 1024 / 1024).toFixed(2);
  log(`✓ ${res.file} — ${res.chapters} гл., ${res.images} илл., ${mb} МБ`);
  if (state.warnings.length) log(`  предупреждений: ${state.warnings.length} (см. state.json)`);
  return 0;
}

main()
  .then((code) => {
    closeUi();
    process.exitCode = code;
  })
  .catch((err) => {
    closeUi();
    console.error(`\n✗ ${err.message}`);
    process.exitCode = 1;
  });
