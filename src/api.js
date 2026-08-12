// Обёртка над публичным API ranobelib (api.cdnlibs.org, Site-Id: 3).
const API = 'https://api.cdnlibs.org/api';
export const SITE = 'https://ranobelib.me';

/** Достаёт slug вида "26690--omniscient-readers-viewpoint-novel" из ссылки или строки. */
export function parseSlug(input) {
  const s = String(input).trim();
  const m = s.match(/ranobelib\.me\/(?:[a-z]{2}\/)?(?:ru\/)?(?:book\/)?([^/?#]+)/i);
  if (m) return decodeURIComponent(m[1]);
  if (/^[\w-]+$/.test(s)) return s;
  return null;
}

export class Api {
  constructor(client) {
    this.client = client;
  }

  async search(query) {
    const url = `${API}/manga?${new URLSearchParams({ q: query, 'site_id[]': '3' })}`;
    const res = await this.client.request(url);
    return res.data ?? [];
  }

  async manga(slug) {
    const fields = ['summary', 'authors', 'publisher', 'genres', 'tags', 'teams', 'releaseDate']
      .map((f) => `fields[]=${f}`)
      .join('&');
    const res = await this.client.request(`${API}/manga/${encodeURIComponent(slug)}?${fields}`);
    return res.data;
  }

  async chapters(slug) {
    const res = await this.client.request(`${API}/manga/${encodeURIComponent(slug)}/chapters`);
    return res.data ?? [];
  }

  /**
   * Ветки перевода как их видит сайт: собственное имя ветки и все её команды.
   * В списке глав у каждой главы указана только команда, залившая эту главу,
   * поэтому подписи вкладок на сайте берутся именно отсюда.
   */
  async branches(mangaId) {
    try {
      const res = await this.client.request(`${API}/branches/${mangaId}`);
      return res.data ?? [];
    } catch {
      // Эндпоинт необязательный: без него просто останутся подписи по главам.
      return [];
    }
  }

  async chapter(slug, { volume, number, branchId }) {
    const params = new URLSearchParams({ volume: String(volume), number: String(number) });
    if (branchId != null) params.set('branch_id', String(branchId));
    const res = await this.client.request(
      `${API}/manga/${encodeURIComponent(slug)}/chapter?${params}`,
    );
    return res.data;
  }

  /** Картинки лежат на самом сайте; на CDN обложек они отдают 403. */
  async binary(url) {
    const abs = url.startsWith('http') ? url : `${SITE}${url.startsWith('/') ? '' : '/'}${url}`;
    return this.client.request(abs, { raw: true });
  }
}

/**
 * Сводит ветки перевода. Названия берутся из карточек веток (как на вкладках сайта),
 * а главы — из списка глав; ветка без глав тоже попадает в список, с count = 0.
 * @param {Array} chapters ответ /chapters
 * @param {Array} [branchMeta] ответ /branches/{id}
 * @returns {Array<{id: number|null, label: string, teams: string[], uploaders: string[], count: number}>}
 */
export function collectBranches(chapters, branchMeta = []) {
  const map = new Map();
  const keyOf = (id) => (id == null ? 'null' : String(id));

  for (const b of branchMeta) {
    const teams = (b.teams ?? []).map((t) => t.name).filter(Boolean);
    map.set(keyOf(b.id), {
      id: b.id ?? null,
      name: b.name || '',
      teams,
      uploaders: [],
      count: 0,
      label: '',
    });
  }

  for (const ch of chapters) {
    for (const b of ch.branches ?? []) {
      const key = keyOf(b.branch_id);
      let entry = map.get(key);
      if (!entry) {
        // Ветки нет в карточках (или /branches недоступен) — собираем по главам.
        entry = {
          id: b.branch_id ?? null,
          name: '',
          teams: (b.teams ?? []).map((t) => t.name).filter(Boolean),
          uploaders: [],
          count: 0,
          label: '',
        };
        map.set(key, entry);
      }
      // Команда главы — резервная подпись, если у ветки не проставлены команды.
      for (const t of b.teams ?? []) {
        if (t.name && !entry.teams.includes(t.name)) entry.teams.push(t.name);
      }
      const user = b.user?.username;
      if (user && !entry.uploaders.includes(user)) entry.uploaders.push(user);
      entry.count += 1;
    }
  }

  for (const entry of map.values()) {
    entry.label = entry.teams.join(' & ') || entry.name || entry.uploaders[0] || 'Неизвестный';
  }

  return [...map.values()].sort((a, b) => b.count - a.count);
}

/** Главы выбранной ветки, в порядке чтения. */
export function chaptersOfBranch(chapters, branchId) {
  const wanted = branchId == null ? 'null' : String(branchId);
  return chapters
    .filter((ch) => (ch.branches ?? []).some((b) => (b.branch_id == null ? 'null' : String(b.branch_id)) === wanted))
    .map((ch) => ({
      index: ch.index,
      volume: ch.volume,
      number: ch.number,
      name: ch.name || '',
      id: ch.id,
    }))
    .sort((a, b) => a.index - b.index);
}
