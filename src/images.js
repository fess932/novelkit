// Пережатие иллюстраций на этапе сборки EPUB через ImageMagick.
// Оригиналы в кэше не трогаются: результат кладётся в отдельный каталог,
// поэтому книгу всегда можно пересобрать с другими настройками.
import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';

/** ImageMagick 7 (magick) или 6 (convert); без него пережатия не будет. */
export function findMagick() {
  for (const [bin, args] of [['magick', ['--version']], ['convert', ['-version']]]) {
    try {
      execFileSync(bin, args, { stdio: 'ignore' });
      return bin;
    } catch {
      /* пробуем следующий */
    }
  }
  return null;
}

function probe(bin, file) {
  const isIm7 = bin === 'magick';
  const out = execFileSync(isIm7 ? 'magick' : 'identify', [
    ...(isIm7 ? ['identify'] : []),
    '-format', '%w %h %A',
    `${file}[0]`,
  ], { encoding: 'utf8' });
  const [w, h, alpha] = out.trim().split(/\s+/);
  return { width: Number(w) || 0, height: Number(h) || 0, alpha: /true|blend/i.test(alpha || '') };
}

/**
 * @param {{dir: string, maxSize: number, quality: number,
 *          bin: string, log?: (m: string) => void}} opts
 */
export function createOptimizer({ dir, maxSize, quality, bin, log }) {
  const outDir = path.join(dir, `assets-jpeg-${maxSize}-${quality}`);
  fs.mkdirSync(outDir, { recursive: true });
  const stats = { before: 0, after: 0, converted: 0, skipped: 0 };
  const cache = new Map();

  const resolve = (name, ext) => {
    if (cache.has(name)) return cache.get(name);

    const src = path.join(dir, 'assets', name);
    const original = { file: src, name, ext };
    if (!fs.existsSync(src)) return original;

    const srcSize = fs.statSync(src).size;
    stats.before += srcSize;

    const keepAsIs = () => {
      stats.after += srcSize;
      stats.skipped += 1;
      cache.set(name, original);
      return original;
    };

    // Анимацию gif пережатие убило бы; svg — это векторы, жать нечего.
    if (ext === 'gif' || ext === 'svg') return keepAsIs();

    let result = original;
    try {
      const { width, height, alpha } = probe(bin, src);
      const needResize = Math.max(width, height) > maxSize;

      // Картинку с прозрачностью в jpeg переводить нельзя — фон станет чёрным,
      // поэтому такие остаются png и только уменьшаются.
      const target = alpha ? 'png' : 'jpg';
      if (target === 'png' && !needResize) return keepAsIs();

      const outName = `${name.replace(/\.[^.]+$/, '')}.${target}`;
      const dest = path.join(outDir, outName);

      if (!fs.existsSync(dest)) {
        const args = [src, '-auto-orient', '-strip'];
        if (needResize) args.push('-resize', `${maxSize}x${maxSize}>`);
        if (target === 'jpg') {
          args.push('-quality', String(quality), '-sampling-factor', '4:2:0', '-interlace', 'Plane');
        } else {
          args.push('-define', 'png:compression-level=9');
        }
        execFileSync(bin, [...args, dest], { stdio: 'ignore' });
      }

      const outSize = fs.statSync(dest).size;
      // Бывает, что «сжатая» версия тяжелее исходника — тогда берём исходник.
      if (outSize >= srcSize) return keepAsIs();

      result = { file: dest, name: outName, ext: target };
      stats.after += outSize;
      stats.converted += 1;
    } catch (err) {
      log?.(`  ! не удалось пережать ${name}: ${err.message.split('\n')[0]}`);
      return keepAsIs();
    }

    cache.set(name, result);
    return result;
  };

  return { resolve, stats };
}
