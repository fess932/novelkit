// Сборка EPUB 3 (с ncx-оглавлением для совместимости со старыми читалками).
import { randomUUID } from 'node:crypto';
import { ZipWriter } from './zip.js';
import { esc } from './render.js';

const CSS = `@charset "utf-8";

body {
  margin: 0 5%;
  line-height: 1.5;
  text-align: justify;
  hyphens: auto;
  -webkit-hyphens: auto;
}

h1, h2, h3 {
  text-align: left;
  line-height: 1.25;
  page-break-after: avoid;
  break-after: avoid;
}

h1.chapter-title {
  font-size: 1.35em;
  margin: 1em 0 0.2em;
}

p.chapter-meta {
  margin: 0 0 1.6em;
  font-size: 0.85em;
  color: #666;
  text-align: left;
}

p {
  margin: 0;
  text-indent: 1.2em;
}

p + p {
  margin-top: 0.15em;
}

p.empty {
  text-indent: 0;
  margin: 0.6em 0;
}

p.note {
  text-indent: 0;
  margin: 0.6em 0;
  font-size: 0.9em;
  color: #555;
}

blockquote {
  margin: 0.8em 1.5em;
  font-style: italic;
}

blockquote p {
  text-indent: 0;
}

hr {
  border: 0;
  border-top: 1px solid currentColor;
  opacity: 0.35;
  margin: 1.4em 20%;
}

div.img {
  text-align: center;
  text-indent: 0;
  margin: 1em 0;
  page-break-inside: avoid;
  break-inside: avoid;
}

div.img img {
  max-width: 100%;
  max-height: 100%;
}

ul, ol {
  margin: 0.6em 0 0.6em 1.4em;
  padding: 0;
}

li {
  text-indent: 0;
}

table {
  border-collapse: collapse;
  margin: 1em auto;
}

td, th {
  border: 1px solid #999;
  padding: 0.3em 0.5em;
  text-indent: 0;
}

/* Титульная страница */
.title-page {
  text-align: center;
  margin-top: 15%;
}

.title-page h1 {
  font-size: 1.8em;
  text-align: center;
  margin-bottom: 0.2em;
}

.title-page .orig {
  font-size: 1em;
  color: #666;
  margin-bottom: 2em;
  text-indent: 0;
}

.title-page .meta {
  text-indent: 0;
  margin: 0.35em 0;
}

.annotation {
  margin-top: 2.5em;
  text-align: left;
}

.annotation h2 {
  font-size: 1.05em;
}
`;

const MIME_BY_EXT = {
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  png: 'image/png',
  gif: 'image/gif',
  webp: 'image/webp',
  svg: 'image/svg+xml',
};

export function mimeForExt(ext) {
  return MIME_BY_EXT[String(ext || '').toLowerCase().replace(/^\./, '')] || 'image/jpeg';
}

function xhtml(title, body, { cssPath = '../styles/main.css' } = {}) {
  return `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="ru" lang="ru">
<head>
  <meta charset="utf-8"/>
  <title>${esc(title)}</title>
  <link rel="stylesheet" type="text/css" href="${cssPath}"/>
</head>
<body>
${body}
</body>
</html>
`;
}

/** Человекочитаемый заголовок главы. */
export function chapterTitle(ch) {
  const num = `Глава ${ch.number}`;
  const name = (ch.name || '').trim();
  return name ? `${num}. ${name}` : num;
}

/**
 * @param {{
 *   meta: {title: string, origTitle?: string, authors?: string[], translators?: string[],
 *          summary?: string, genres?: string[], year?: string, sourceUrl?: string, publisher?: string},
 *   chapters: Array<{volume: string|number, number: string|number, name: string, body: string}>,
 *   images: Array<{path: string, data: Buffer, ext: string}>,
 *   cover?: {data: Buffer, ext: string} | null,
 * }} book
 * @returns {Buffer}
 */
export function buildEpub(book) {
  const { meta, chapters, images = [], cover = null } = book;
  const zip = new ZipWriter();
  const uid = `urn:uuid:${randomUUID()}`;
  const modified = new Date().toISOString().replace(/\.\d+Z$/, 'Z');

  zip.add('mimetype', 'application/epub+zip', { store: true });
  zip.add(
    'META-INF/container.xml',
    `<?xml version="1.0" encoding="utf-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>
`,
  );
  zip.add('OEBPS/styles/main.css', CSS);

  const manifest = [];
  const spine = [];

  if (cover) {
    const coverName = `images/cover.${cover.ext}`;
    zip.add(`OEBPS/${coverName}`, cover.data);
    manifest.push(
      `<item id="cover-image" href="${coverName}" media-type="${mimeForExt(cover.ext)}" properties="cover-image"/>`,
    );
    zip.add(
      'OEBPS/text/cover.xhtml',
      xhtml(
        'Обложка',
        `<div class="img" style="margin:0;"><img src="../${coverName}" alt="Обложка"/></div>`,
      ),
    );
    manifest.push('<item id="cover" href="text/cover.xhtml" media-type="application/xhtml+xml"/>');
    spine.push('<itemref idref="cover" linear="yes"/>');
  }

  // Титульная страница
  const infoRows = [];
  if (meta.authors?.length) infoRows.push(`<p class="meta">Автор: ${esc(meta.authors.join(', '))}</p>`);
  if (meta.translators?.length) {
    infoRows.push(`<p class="meta">Перевод: ${esc(meta.translators.join(', '))}</p>`);
  }
  if (meta.year) infoRows.push(`<p class="meta">Год: ${esc(meta.year)}</p>`);
  if (meta.genres?.length) infoRows.push(`<p class="meta">Жанры: ${esc(meta.genres.join(', '))}</p>`);
  if (meta.sourceUrl) infoRows.push(`<p class="meta">Источник: ${esc(meta.sourceUrl)}</p>`);

  const annotation = meta.summary
    ? `<div class="annotation"><h2>Аннотация</h2>${meta.summary
        .split(/\n{2,}|\r\n{2,}/)
        .map((p) => `<p>${esc(p.trim())}</p>`)
        .join('\n')}</div>`
    : '';

  zip.add(
    'OEBPS/text/title.xhtml',
    xhtml(
      meta.title,
      `<div class="title-page">
  <h1>${esc(meta.title)}</h1>
  ${meta.origTitle ? `<p class="orig">${esc(meta.origTitle)}</p>` : ''}
  ${infoRows.join('\n  ')}
</div>
${annotation}`,
    ),
  );
  manifest.push('<item id="title" href="text/title.xhtml" media-type="application/xhtml+xml"/>');
  spine.push('<itemref idref="title" linear="yes"/>');

  // Главы
  const navItems = [];
  chapters.forEach((ch, i) => {
    const id = `ch${String(i + 1).padStart(4, '0')}`;
    const href = `text/${id}.xhtml`;
    const title = chapterTitle(ch);
    const volLabel = ch.volume != null && ch.volume !== '' ? `Том ${ch.volume}` : '';
    zip.add(
      `OEBPS/${href}`,
      xhtml(
        title,
        `<h1 class="chapter-title">${esc(title)}</h1>
${volLabel ? `<p class="chapter-meta">${esc(volLabel)}</p>` : ''}
${ch.body}`,
      ),
    );
    manifest.push(`<item id="${id}" href="${href}" media-type="application/xhtml+xml"/>`);
    spine.push(`<itemref idref="${id}" linear="yes"/>`);
    navItems.push({ id, href, title, volume: String(ch.volume ?? '') });
  });

  for (const img of images) {
    zip.add(`OEBPS/${img.path}`, img.data);
    manifest.push(
      `<item id="${img.path.replace(/[^\w]/g, '_')}" href="${img.path}" media-type="${mimeForExt(img.ext)}"/>`,
    );
  }

  // Оглавление: группировка по томам, если томов больше одного.
  const volumes = [...new Set(navItems.map((n) => n.volume))].filter((v) => v !== '');
  const grouped = volumes.length > 1;
  let navList;
  if (grouped) {
    navList = volumes
      .map((vol) => {
        const items = navItems.filter((n) => n.volume === vol);
        return `      <li><span>Том ${esc(vol)}</span>
        <ol>
${items.map((n) => `          <li><a href="${n.href}">${esc(n.title)}</a></li>`).join('\n')}
        </ol>
      </li>`;
      })
      .join('\n');
  } else {
    navList = navItems
      .map((n) => `      <li><a href="${n.href}">${esc(n.title)}</a></li>`)
      .join('\n');
  }

  zip.add(
    'OEBPS/nav.xhtml',
    `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="ru" lang="ru">
<head>
  <meta charset="utf-8"/>
  <title>Оглавление</title>
  <link rel="stylesheet" type="text/css" href="styles/main.css"/>
</head>
<body>
  <nav epub:type="toc" id="toc">
    <h1>Оглавление</h1>
    <ol>
      <li><a href="text/title.xhtml">${esc(meta.title)}</a></li>
${navList}
    </ol>
  </nav>
  <nav epub:type="landmarks" hidden="hidden">
    <ol>
      <li><a epub:type="bodymatter" href="${navItems[0]?.href ?? 'text/title.xhtml'}">Начало</a></li>
    </ol>
  </nav>
</body>
</html>
`,
  );
  manifest.push('<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>');

  // ncx для читалок, не понимающих EPUB 3
  const navPoints = navItems
    .map(
      (n, i) => `    <navPoint id="np-${n.id}" playOrder="${i + 2}">
      <navLabel><text>${esc(n.title)}</text></navLabel>
      <content src="${n.href}"/>
    </navPoint>`,
    )
    .join('\n');
  zip.add(
    'OEBPS/toc.ncx',
    `<?xml version="1.0" encoding="utf-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <head>
    <meta name="dtb:uid" content="${uid}"/>
    <meta name="dtb:depth" content="1"/>
    <meta name="dtb:totalPageCount" content="0"/>
    <meta name="dtb:maxPageNumber" content="0"/>
  </head>
  <docTitle><text>${esc(meta.title)}</text></docTitle>
  <navMap>
    <navPoint id="np-title" playOrder="1">
      <navLabel><text>${esc(meta.title)}</text></navLabel>
      <content src="text/title.xhtml"/>
    </navPoint>
${navPoints}
  </navMap>
</ncx>
`,
  );
  manifest.push('<item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>');
  manifest.push('<item id="css" href="styles/main.css" media-type="text/css"/>');

  const creators = (meta.authors?.length ? meta.authors : ['Неизвестный автор'])
    .map((a, i) => `    <dc:creator id="creator-${i}">${esc(a)}</dc:creator>`)
    .join('\n');
  const contributors = (meta.translators ?? [])
    .map((t, i) => `    <dc:contributor id="contrib-${i}">${esc(t)}</dc:contributor>`)
    .join('\n');
  const subjects = (meta.genres ?? []).map((g) => `    <dc:subject>${esc(g)}</dc:subject>`).join('\n');

  zip.add(
    'OEBPS/content.opf',
    `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid" xml:lang="ru">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="bookid">${uid}</dc:identifier>
    <dc:title>${esc(meta.title)}</dc:title>
    <dc:language>ru</dc:language>
${creators}
${contributors}
${subjects}
    ${meta.publisher ? `<dc:publisher>${esc(meta.publisher)}</dc:publisher>` : ''}
    ${meta.summary ? `<dc:description>${esc(meta.summary.slice(0, 4000))}</dc:description>` : ''}
    ${meta.sourceUrl ? `<dc:source>${esc(meta.sourceUrl)}</dc:source>` : ''}
    ${meta.year ? `<dc:date>${esc(meta.year)}</dc:date>` : ''}
    <meta property="dcterms:modified">${modified}</meta>
    ${cover ? '<meta name="cover" content="cover-image"/>' : ''}
  </metadata>
  <manifest>
${manifest.map((m) => `    ${m}`).join('\n')}
  </manifest>
  <spine toc="ncx">
${spine.map((s) => `    ${s}`).join('\n')}
  </spine>
</package>
`,
  );

  return zip.toBuffer();
}
