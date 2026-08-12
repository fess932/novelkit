// Приведение контента главы к чистому XHTML.
// Сайт отдаёт контент в двух видах: ProseMirror-документ (JSON) и HTML-строка.

const VOID = new Set(['br', 'hr', 'img']);

// Теги, которые имеет смысл нести в книгу. Всё остальное разворачивается
// (содержимое сохраняется, обёртка выбрасывается) — так не теряется текст.
const ALLOWED = new Map([
  ['p', 'p'], ['br', 'br'], ['hr', 'hr'],
  ['b', 'strong'], ['strong', 'strong'],
  ['i', 'em'], ['em', 'em'],
  ['u', 'u'], ['s', 's'], ['strike', 's'], ['del', 'del'], ['ins', 'ins'],
  ['sup', 'sup'], ['sub', 'sub'],
  ['blockquote', 'blockquote'],
  ['h1', 'h2'], ['h2', 'h2'], ['h3', 'h3'], ['h4', 'h4'], ['h5', 'h5'], ['h6', 'h6'],
  ['ul', 'ul'], ['ol', 'ol'], ['li', 'li'],
  ['img', 'img'], ['a', 'a'],
  ['code', 'code'], ['pre', 'pre'],
  ['table', 'table'], ['thead', 'thead'], ['tbody', 'tbody'],
  ['tr', 'tr'], ['td', 'td'], ['th', 'th'],
  ['figure', 'figure'], ['figcaption', 'figcaption'],
]);

// Теги, содержимое которых выбрасывается целиком.
const DROP = new Set(['script', 'style', 'iframe', 'noscript', 'svg', 'form', 'button', 'input']);

const NAMED_ENTITIES = {
  amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: ' ',
  laquo: '«', raquo: '»', ldquo: '“', rdquo: '”',
  lsquo: '‘', rsquo: '’', mdash: '—', ndash: '–',
  hellip: '…', bull: '•', middot: '·', deg: '°', copy: '©', reg: '®',
  trade: '™', times: '×', divide: '÷', shy: '­', ensp: ' ', emsp: ' ', thinsp: ' ',
};

export function decodeEntities(str) {
  return String(str).replace(/&(#x?[0-9a-fA-F]+|[a-zA-Z][a-zA-Z0-9]*);/g, (m, body) => {
    if (body[0] === '#') {
      const code = body[1] === 'x' || body[1] === 'X'
        ? parseInt(body.slice(2), 16)
        : parseInt(body.slice(1), 10);
      if (Number.isFinite(code) && code > 0 && code <= 0x10ffff) {
        try {
          return String.fromCodePoint(code);
        } catch {
          return m;
        }
      }
      return m;
    }
    const named = NAMED_ENTITIES[body];
    return named === undefined ? m : named;
  });
}

export function esc(str) {
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

function escAttr(str) {
  return esc(str).replace(/"/g, '&quot;');
}

/** Убирает управляющие символы, недопустимые в XML 1.0. */
function stripInvalid(str) {
  // eslint-disable-next-line no-control-regex
  return str.replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\uFFFE\uFFFF]/g, '');
}

// --- ProseMirror ------------------------------------------------------------

const MARK_TAGS = {
  bold: 'strong', strong: 'strong',
  italic: 'em', em: 'em',
  underline: 'u',
  strike: 's', strikethrough: 's',
  superscript: 'sup', subscript: 'sub',
  code: 'code',
};

function renderMarks(text, marks) {
  let out = esc(text);
  for (const mark of [...(marks ?? [])].reverse()) {
    if (mark.type === 'link') {
      const href = mark.attrs?.href;
      if (href) out = `<a href="${escAttr(href)}">${out}</a>`;
      continue;
    }
    const tag = MARK_TAGS[mark.type];
    if (tag) out = `<${tag}>${out}</${tag}>`;
  }
  return out;
}

function renderPmNode(node, ctx) {
  if (!node || typeof node !== 'object') return '';
  const kids = () => (node.content ?? []).map((n) => renderPmNode(n, ctx)).join('');

  switch (node.type) {
    case 'doc':
      return kids();
    case 'text':
      return renderMarks(node.text ?? '', node.marks);
    case 'paragraph': {
      const inner = kids().trim();
      // Пустые абзацы сайт использует как отбивки — сохраняем их как разделители.
      return inner ? `<p>${inner}</p>\n` : '<p class="empty"> </p>\n';
    }
    case 'heading': {
      const level = Math.min(6, Math.max(2, Number(node.attrs?.level) || 2));
      return `<h${level}>${kids()}</h${level}>\n`;
    }
    case 'blockquote':
      return `<blockquote>\n${kids()}</blockquote>\n`;
    case 'bulletList':
      return `<ul>\n${kids()}</ul>\n`;
    case 'orderedList':
      return `<ol>\n${kids()}</ol>\n`;
    case 'listItem':
      return `<li>${kids()}</li>\n`;
    case 'horizontalRule':
      return '<hr/>\n';
    case 'hardBreak':
      return '<br/>';
    case 'codeBlock':
      return `<pre><code>${kids()}</code></pre>\n`;
    case 'image':
      return renderPmImage(node, ctx);
    default:
      return kids();
  }
}

function renderPmImage(node, ctx) {
  const parts = [];
  const description = (node.attrs?.description ?? '').trim();
  for (const item of node.attrs?.images ?? []) {
    const att = ctx.attachments.get(item.image) ?? ctx.attachments.get(`${item.image}`);
    if (!att) continue;
    const local = ctx.images.add(att.url, att.extension);
    if (!local) continue;
    parts.push(`<div class="img"><img src="${escAttr(local)}" alt=""/></div>`);
  }
  if (description) {
    // Подпись у сайта часто содержит примечание переводчика — это текст, его нельзя терять.
    parts.push(`<p class="note">${esc(description)}</p>`);
  }
  return parts.length ? `${parts.join('\n')}\n` : '';
}

// --- HTML -------------------------------------------------------------------

function parseAttrs(src) {
  const attrs = {};
  const re = /([a-zA-Z_:][\w:.-]*)\s*=\s*("([^"]*)"|'([^']*)'|([^\s"'=<>`]+))/g;
  let m;
  while ((m = re.exec(src))) {
    attrs[m[1].toLowerCase()] = decodeEntities(m[3] ?? m[4] ?? m[5] ?? '');
  }
  return attrs;
}

function renderHtml(html, ctx) {
  const out = [];
  const stack = [];
  let dropDepth = 0;
  let dropTag = null;

  const tokenizer = /<!--[\s\S]*?-->|<\/?\s*([a-zA-Z][a-zA-Z0-9]*)((?:"[^"]*"|'[^']*'|[^>])*)>/g;
  let last = 0;
  let m;

  const pushText = (raw) => {
    if (dropDepth > 0) return;
    const text = esc(decodeEntities(raw));
    if (text) out.push(text);
  };

  while ((m = tokenizer.exec(html))) {
    pushText(html.slice(last, m.index));
    last = tokenizer.lastIndex;

    if (m[0].startsWith('<!--')) continue;

    const tag = (m[1] || '').toLowerCase();
    const closing = m[0][1] === '/';
    const selfClosing = /\/\s*>$/.test(m[0]);

    if (DROP.has(tag)) {
      if (closing) {
        if (dropTag === tag && dropDepth > 0) dropDepth -= 1;
        if (dropDepth === 0) dropTag = null;
      } else if (!selfClosing) {
        dropDepth += 1;
        dropTag = tag;
      }
      continue;
    }
    if (dropDepth > 0) continue;

    const mapped = ALLOWED.get(tag);
    if (!mapped) continue; // неизвестная обёртка — разворачиваем, текст остаётся

    if (closing) {
      const at = stack.lastIndexOf(mapped);
      if (at === -1) continue;
      // Закрываем всё, что осталось незакрытым внутри — иначе XHTML не провалидируется.
      while (stack.length > at) out.push(`</${stack.pop()}>`);
      continue;
    }

    const attrs = parseAttrs(m[2] || '');

    if (mapped === 'img') {
      const src = attrs.src || attrs['data-src'] || '';
      if (!src) continue;
      const local = ctx.images.add(src);
      if (local) out.push(`<div class="img"><img src="${escAttr(local)}" alt="${escAttr(attrs.alt || '')}"/></div>`);
      continue;
    }
    if (VOID.has(mapped)) {
      out.push(`<${mapped}/>`);
      continue;
    }
    if (mapped === 'a') {
      const href = attrs.href || '';
      // Ссылку без адреса (или с javascript:) разворачиваем: текст остаётся, обёртки нет.
      if (!href || /^\s*javascript:/i.test(href)) continue;
      out.push(`<a href="${escAttr(href)}">`);
      stack.push('a');
      continue;
    }

    out.push(`<${mapped}>`);
    if (!selfClosing) stack.push(mapped);
  }

  pushText(html.slice(last));
  while (stack.length) out.push(`</${stack.pop()}>`);

  return out.join('');
}

/**
 * Достаёт простой текст из чего угодно: ProseMirror-документа, HTML-строки или обычной строки.
 * Аннотация книги приходит в тех же двух видах, что и контент главы.
 */
export function plainText(value) {
  if (!value) return '';

  if (typeof value === 'string') {
    const trimmed = value.trim();
    if (trimmed.startsWith('{')) {
      try {
        return plainText(JSON.parse(trimmed));
      } catch {
        /* не JSON — разбираем как html */
      }
    }
    return decodeEntities(
      trimmed
        .replace(/<br\s*\/?>/gi, '\n')
        .replace(/<\/(p|div|li|h[1-6])>/gi, '\n\n')
        .replace(/<[^>]+>/g, ''),
    )
      .replace(/\n{3,}/g, '\n\n')
      .trim();
  }

  if (typeof value !== 'object') return String(value);

  const parts = [];
  const walk = (node) => {
    if (Array.isArray(node)) {
      node.forEach(walk);
      return;
    }
    if (!node || typeof node !== 'object') return;
    if (node.type === 'text') parts.push(node.text ?? '');
    else if (node.type === 'hardBreak') parts.push('\n');
    walk(node.content);
    // Блочные узлы разделяем пустой строкой — иначе абзацы слипнутся.
    if (['paragraph', 'heading', 'blockquote', 'listItem'].includes(node.type)) parts.push('\n\n');
  };
  walk(value);

  return parts.join('').replace(/\n{3,}/g, '\n\n').trim();
}

// --- Точка входа ------------------------------------------------------------

/**
 * @param {object|string} content содержимое главы из API
 * @param {{attachments?: Array, images: {add: (url: string, ext?: string) => string|null}}} ctx
 * @returns {string} XHTML-фрагмент тела главы
 */
export function renderContent(content, ctx) {
  const attachments = new Map();
  for (const a of ctx.attachments ?? []) {
    if (a?.name) attachments.set(a.name, a);
    if (a?.filename) attachments.set(a.filename, a);
  }
  const inner = { attachments, images: ctx.images };

  let body;
  if (content && typeof content === 'object') {
    body = renderPmNode(content, inner);
  } else if (typeof content === 'string') {
    const trimmed = content.trim();
    if (trimmed.startsWith('{')) {
      // Иногда ProseMirror-документ приходит строкой с JSON внутри.
      try {
        body = renderPmNode(JSON.parse(trimmed), inner);
      } catch {
        body = renderHtml(trimmed, inner);
      }
    } else {
      body = renderHtml(trimmed, inner);
    }
  } else {
    body = '';
  }

  body = stripInvalid(body).trim();
  return body || '<p class="empty"> </p>';
}
