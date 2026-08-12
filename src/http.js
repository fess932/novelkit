// HTTP-слой: единая очередь запросов с паузами, ретраями и уважением к 429.
const UA =
  'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36';

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export class HttpError extends Error {
  constructor(message, { status = 0, url = '', retryable = false } = {}) {
    super(message);
    this.name = 'HttpError';
    this.status = status;
    this.url = url;
    this.retryable = retryable;
  }
}

export class Client {
  /**
   * @param {{delay?: number, jitter?: number, retries?: number, timeout?: number,
   *          maxDelay?: number, onNotice?: (msg: string) => void}} [opts]
   */
  constructor(opts = {}) {
    this.baseDelay = opts.delay ?? 1500;
    this.delay = this.baseDelay;
    this.jitter = opts.jitter ?? 700;
    this.retries = opts.retries ?? 4;
    this.timeout = opts.timeout ?? 30000;
    this.maxDelay = opts.maxDelay ?? 30000;
    this.onNotice = opts.onNotice ?? (() => {});
    this.lastRequestAt = 0;
    this.queue = Promise.resolve();
  }

  // Пауза между запросами: базовая задержка + случайный джиттер.
  // Джиттер размывает регулярность, из-за которой запросы легко ловят рейт-лимит.
  #pause() {
    const wait = this.delay + Math.floor(Math.random() * this.jitter);
    const elapsed = Date.now() - this.lastRequestAt;
    return Math.max(0, wait - elapsed);
  }

  // После 429 держим повышенный темп какое-то время, затем плавно возвращаемся к базовому.
  #slowDown() {
    this.delay = Math.min(this.maxDelay, Math.round(Math.max(this.delay, 1000) * 1.7));
    this.onNotice(`рейт-лимит: пауза между запросами увеличена до ${this.delay} мс`);
  }

  #speedUp() {
    if (this.delay > this.baseDelay) {
      this.delay = Math.max(this.baseDelay, Math.round(this.delay * 0.85));
    }
  }

  /** Все запросы идут по одной очереди — параллельных обращений к сайту нет. */
  request(url, { headers = {}, raw = false } = {}) {
    const task = this.queue.then(() => this.#doRequest(url, { headers, raw }));
    // Очередь не должна ломаться из-за упавшего запроса.
    this.queue = task.then(
      () => undefined,
      () => undefined,
    );
    return task;
  }

  async #doRequest(url, { headers, raw }) {
    let attempt = 0;
    for (;;) {
      const pause = this.#pause();
      if (pause > 0) await sleep(pause);
      this.lastRequestAt = Date.now();

      let res;
      try {
        res = await fetch(url, {
          headers: {
            'User-Agent': UA,
            Accept: raw ? '*/*' : 'application/json',
            'Accept-Language': 'ru-RU,ru;q=0.9',
            Referer: 'https://ranobelib.me/',
            Origin: 'https://ranobelib.me',
            'Site-Id': '3',
            ...headers,
          },
          signal: AbortSignal.timeout(this.timeout),
        });
      } catch (err) {
        // Сеть/таймаут — ретраим.
        if (attempt++ < this.retries) {
          const back = Math.min(this.maxDelay, 1000 * 2 ** attempt);
          this.onNotice(`сеть: ${err.message}; повтор через ${back} мс (${attempt}/${this.retries})`);
          await sleep(back);
          continue;
        }
        throw new HttpError(`сетевая ошибка: ${err.message}`, { url, retryable: true });
      }

      if (res.status === 429 || res.status === 503) {
        this.#slowDown();
        if (attempt++ < this.retries) {
          const retryAfter = Number(res.headers.get('retry-after'));
          const back = Number.isFinite(retryAfter) && retryAfter > 0
            ? retryAfter * 1000
            : Math.min(this.maxDelay, 2000 * 2 ** attempt);
          this.onNotice(`HTTP ${res.status}; ждём ${Math.round(back / 1000)} с (${attempt}/${this.retries})`);
          await sleep(back);
          continue;
        }
        throw new HttpError(`HTTP ${res.status} — сработал рейт-лимит`, {
          status: res.status,
          url,
          retryable: true,
        });
      }

      if (res.status >= 500) {
        if (attempt++ < this.retries) {
          const back = Math.min(this.maxDelay, 1500 * 2 ** attempt);
          this.onNotice(`HTTP ${res.status}; повтор через ${back} мс (${attempt}/${this.retries})`);
          await sleep(back);
          continue;
        }
        throw new HttpError(`HTTP ${res.status}`, { status: res.status, url, retryable: true });
      }

      if (!res.ok) {
        // 4xx (кроме 429) не ретраим: платная/удалённая глава, опечатка в слаге и т.п.
        let detail = '';
        try {
          const body = await res.text();
          const json = JSON.parse(body);
          detail = json?.message || json?.error || body.slice(0, 200);
        } catch {
          /* тело не JSON — деталей не будет */
        }
        throw new HttpError(`HTTP ${res.status}${detail ? `: ${detail}` : ''}`, {
          status: res.status,
          url,
        });
      }

      this.#speedUp();
      if (raw) {
        const buf = Buffer.from(await res.arrayBuffer());
        return { buffer: buf, contentType: res.headers.get('content-type') || '' };
      }
      return res.json();
    }
  }
}
