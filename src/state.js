// Состояние задания: кэш скачанного и точка продолжения после обрыва.
import fs from 'node:fs';
import path from 'node:path';
import { createHash } from 'node:crypto';

export const STATE_VERSION = 1;

export function jobDirFor(root, slug, branchId) {
  const safe = String(slug).replace(/[^\w.-]/g, '_');
  return path.join(root, `${safe}--b${branchId ?? 'default'}`);
}

export function ensureJob(dir) {
  fs.mkdirSync(path.join(dir, 'raw'), { recursive: true });
  fs.mkdirSync(path.join(dir, 'assets'), { recursive: true });
}

export function loadState(dir) {
  const file = path.join(dir, 'state.json');
  if (!fs.existsSync(file)) return null;
  const state = JSON.parse(fs.readFileSync(file, 'utf8'));
  if (state.version !== STATE_VERSION) {
    throw new Error(
      `состояние в ${dir} записано другой версией утилиты (v${state.version}); удалите каталог и начните заново`,
    );
  }
  return state;
}

/** Запись через временный файл: обрыв на середине не оставит битый state.json. */
export function saveState(dir, state) {
  const file = path.join(dir, 'state.json');
  const tmp = `${file}.tmp`;
  fs.writeFileSync(tmp, JSON.stringify(state, null, 2));
  fs.renameSync(tmp, file);
}

export function rawPath(dir, index) {
  return path.join(dir, 'raw', `${String(index).padStart(5, '0')}.json`);
}

export function saveRaw(dir, index, data) {
  const file = rawPath(dir, index);
  const tmp = `${file}.tmp`;
  fs.writeFileSync(tmp, JSON.stringify(data));
  fs.renameSync(tmp, file);
}

export function loadRaw(dir, index) {
  return JSON.parse(fs.readFileSync(rawPath(dir, index), 'utf8'));
}

export function hasRaw(dir, index) {
  return fs.existsSync(rawPath(dir, index));
}

/** Имя файла картинки, устойчивое между запусками: зависит только от URL. */
export function assetName(url, ext) {
  const hash = createHash('sha1').update(url).digest('hex').slice(0, 12);
  const clean = String(ext || '')
    .toLowerCase()
    .replace(/^\./, '')
    .replace(/[^a-z0-9]/g, '');
  return `img-${hash}.${clean || 'jpg'}`;
}

export function assetPath(dir, name) {
  return path.join(dir, 'assets', name);
}

/** Задания, у которых есть незавершённая загрузка. */
export function listJobs(root) {
  if (!fs.existsSync(root)) return [];
  const jobs = [];
  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    const dir = path.join(root, entry.name);
    try {
      const state = loadState(dir);
      if (state) jobs.push({ dir, state, mtime: fs.statSync(path.join(dir, 'state.json')).mtimeMs });
    } catch {
      /* чужая или битая папка — пропускаем */
    }
  }
  return jobs.sort((a, b) => b.mtime - a.mtime);
}

export function progressOf(state) {
  const done = state.chapters.filter((c) => c.done).length;
  return { done, total: state.chapters.length, left: state.chapters.length - done };
}
