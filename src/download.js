// Загрузка глав в кэш задания и сборка EPUB из кэша.
import fs from 'node:fs';
import path from 'node:path';
import { renderContent } from './render.js';
import { buildEpub, chapterTitle } from './epub.js';
import { SITE } from './api.js';
import {
  assetName, assetPath, hasRaw, loadRaw, saveRaw, saveState, progressOf,
} from './state.js';
import { createOptimizer, findMagick } from './images.js';

const EXT_RE = /\.([a-z0-9]{2,5})(?:\?|#|$)/i;

function absUrl(url) {
  if (/^https?:\/\//i.test(url)) return url;
  if (url.startsWith('//')) return `https:${url}`;
  return `${SITE}${url.startsWith('/') ? '' : '/'}${url}`;
}

function extFromUrl(url, fallback) {
  const m = EXT_RE.exec(url);
  return (m ? m[1] : fallback || 'jpg').toLowerCase();
}

/**
 * Реестр картинок: рендер вызывает add() и сразу получает путь внутри EPUB,
 * а сами файлы качаются отдельным шагом.
 */
function makeRegistry(collect) {
  return {
    add(url, ext) {
      if (!url) return null;
      const abs = absUrl(url);
      const extension = extFromUrl(abs, ext);
      const name = assetName(abs, extension);
      collect({ url: abs, name, ext: extension });
      return `../images/${name}`;
    },
  };
}

function fmtDuration(ms) {
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s} с`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} мин ${s % 60} с`;
  return `${Math.floor(m / 60)} ч ${m % 60} мин`;
}

/**
 * Качает недостающие главы. Бросает исключение на первой неустранимой ошибке —
 * всё, что успело скачаться, остаётся в кэше и подхватывается при продолжении.
 */
export async function downloadChapters({ dir, state, api, log, onProgress }) {
  const started = Date.now();
  let downloaded = 0;
  const totalLeft = state.chapters.filter((c) => !c.done).length;

  for (const ch of state.chapters) {
    if (ch.done && hasRaw(dir, ch.index)) continue;

    let data;
    try {
      data = await api.chapter(state.slug, {
        volume: ch.volume,
        number: ch.number,
        branchId: state.branchId,
      });
    } catch (err) {
      err.chapter = ch;
      throw err;
    }
    saveRaw(dir, ch.index, data);

    // Картинки главы: находим их рендером, качаем, кладём рядом с кэшем.
    if (state.images) {
      const found = [];
      const registry = makeRegistry((img) => found.push(img));
      renderContent(data.content, { attachments: data.attachments, images: registry });

      for (const img of found) {
        if (state.assets[img.url] && fs.existsSync(assetPath(dir, state.assets[img.url].name))) continue;
        try {
          const { buffer } = await api.binary(img.url);
          fs.writeFileSync(assetPath(dir, img.name), buffer);
          state.assets[img.url] = { name: img.name, ext: img.ext };
        } catch (err) {
          // Битая картинка не должна ронять загрузку текста на несколько сотен глав.
          const note = `картинка не скачалась (${chapterTitle(ch)}): ${err.message}`;
          state.warnings.push(note);
          log(`  ! ${note}`);
        }
      }
    }

    ch.done = true;
    ch.title = chapterTitle(ch);
    state.updatedAt = new Date().toISOString();
    saveState(dir, state);

    downloaded += 1;
    const { done, total } = progressOf(state);
    const perItem = (Date.now() - started) / downloaded;
    const eta = perItem * (totalLeft - downloaded);
    onProgress?.({
      done,
      total,
      title: ch.title,
      eta: totalLeft - downloaded > 0 ? fmtDuration(eta) : '—',
    });
  }
}

/**
 * Собирает EPUB из того, что лежит в кэше задания.
 * @param {{optimize?: {maxSize: number, quality: number}}} [params.optimize]
 */
export function buildFromCache({ dir, state, outFile, log, optimize = null }) {
  const chapters = [];
  const usedAssets = new Map();
  const missing = [];

  let optimizer = null;
  if (optimize) {
    const bin = findMagick();
    if (!bin) {
      log('  ! ImageMagick не найден — картинки останутся в оригинале (brew install imagemagick)');
    } else {
      optimizer = createOptimizer({ ...optimize, dir, bin, log });
      log(`  сжатие иллюстраций: до ${optimize.maxSize} px `
        + `по большей стороне, качество ${optimize.quality}`);
    }
  }
  // Имя файла в книге может смениться при пережатии (png → jpg) — держим соответствие.
  const renamed = new Map();
  const finalAsset = (name, ext) => {
    if (!optimizer) return { name, ext, file: assetPath(dir, name) };
    if (!renamed.has(name)) renamed.set(name, optimizer.resolve(name, ext));
    return renamed.get(name);
  };

  for (const ch of state.chapters) {
    if (!hasRaw(dir, ch.index)) {
      missing.push(ch);
      continue;
    }
    const data = loadRaw(dir, ch.index);
    const found = new Map();
    const registry = makeRegistry((img) => {
      const known = state.assets[img.url];
      if (!known || !fs.existsSync(assetPath(dir, known.name))) return;
      const final = finalAsset(known.name, known.ext);
      usedAssets.set(final.name, final);
      found.set(known.name, final.name);
    });
    // Картинки, которых нет на диске, из разметки убираем — иначе читалка покажет битую ссылку.
    const wrapped = {
      add(url, ext) {
        const local = registry.add(url, ext);
        if (!local) return null;
        const finalName = found.get(local.split('/').pop());
        return finalName ? `../images/${finalName}` : null;
      },
    };
    const body = renderContent(data.content, { attachments: data.attachments, images: wrapped });
    chapters.push({ volume: ch.volume, number: ch.number, name: ch.name, body });
  }

  if (missing.length) {
    log(`  ! в книгу не попало глав: ${missing.length} (нет в кэше)`);
  }
  if (!chapters.length) throw new Error('нет ни одной скачанной главы — собирать нечего');

  const images = [...usedAssets.values()].map((a) => ({
    path: `images/${a.name}`,
    data: fs.readFileSync(a.file),
    ext: a.ext,
  }));

  let cover = null;
  const coverFile = path.join(dir, 'cover.bin');
  if (fs.existsSync(coverFile) && state.coverExt) {
    cover = { data: fs.readFileSync(coverFile), ext: state.coverExt };
    if (optimizer) {
      // Обложка лежит отдельным файлом, поэтому пережимается тем же путём вручную.
      const tmpName = `cover.${state.coverExt}`;
      const staged = assetPath(dir, tmpName);
      if (!fs.existsSync(staged)) fs.copyFileSync(coverFile, staged);
      const opt = optimizer.resolve(tmpName, state.coverExt);
      cover = { data: fs.readFileSync(opt.file), ext: opt.ext };
    }
  }

  if (optimizer) {
    const { before, after, converted, skipped } = optimizer.stats;
    const saved = before ? (1 - after / before) * 100 : 0;
    log(`  иллюстрации: ${(before / 1024 / 1024).toFixed(1)} → ${(after / 1024 / 1024).toFixed(1)} МБ `
      + `(−${saved.toFixed(0)}%), пережато ${converted}, без изменений ${skipped}`);
  }

  // Старые версии сохраняли описание как "[object Object]" — такое в книгу не пускаем.
  const meta = { ...state.meta };
  if (typeof meta.summary !== 'string' || /^\[object \w+\]$/.test(meta.summary.trim())) {
    if (meta.summary) log('  ! описание починить не удалось, собираю без него');
    meta.summary = '';
  }

  const buf = buildEpub({ meta, chapters, images, cover });
  fs.mkdirSync(path.dirname(path.resolve(outFile)), { recursive: true });
  fs.writeFileSync(outFile, buf);
  return { file: outFile, size: buf.length, chapters: chapters.length, images: images.length };
}
