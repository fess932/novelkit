// Минимальный ZIP-писатель (без зависимостей).
// EPUB требует, чтобы первым в архиве лежал файл mimetype, записанный без сжатия,
// поэтому нужен полный контроль над порядком и методом компрессии.
import zlib from 'node:zlib';

const CRC_TABLE = (() => {
  const t = new Int32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c;
  }
  return t;
})();

function crc32(buf) {
  if (typeof zlib.crc32 === 'function') return zlib.crc32(buf) >>> 0;
  let c = -1;
  for (let i = 0; i < buf.length; i++) c = CRC_TABLE[(c ^ buf[i]) & 0xff] ^ (c >>> 8);
  return (c ^ -1) >>> 0;
}

// DOS-время. Фиксированная метка делает сборку воспроизводимой.
const DOS_TIME = 0x0000;
const DOS_DATE = ((2020 - 1980) << 9) | (1 << 5) | 1;

export class ZipWriter {
  constructor() {
    this.parts = [];
    this.entries = [];
    this.offset = 0;
  }

  #push(buf) {
    this.parts.push(buf);
    this.offset += buf.length;
  }

  /**
   * @param {string} name путь внутри архива
   * @param {Buffer|string} data содержимое
   * @param {{store?: boolean}} [opts] store=true — без сжатия (нужно для mimetype)
   */
  add(name, data, opts = {}) {
    const body = Buffer.isBuffer(data) ? data : Buffer.from(String(data), 'utf8');
    const nameBuf = Buffer.from(name, 'utf8');
    const crc = crc32(body);
    const store = opts.store === true;
    const compressed = store ? body : zlib.deflateRawSync(body, { level: 9 });
    const method = store ? 0 : 8;
    const localOffset = this.offset;

    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4); // версия
    local.writeUInt16LE(0x0800, 6); // UTF-8 имена
    local.writeUInt16LE(method, 8);
    local.writeUInt16LE(DOS_TIME, 10);
    local.writeUInt16LE(DOS_DATE, 12);
    local.writeUInt32LE(crc, 14);
    local.writeUInt32LE(compressed.length, 18);
    local.writeUInt32LE(body.length, 22);
    local.writeUInt16LE(nameBuf.length, 26);
    local.writeUInt16LE(0, 28);

    this.#push(local);
    this.#push(nameBuf);
    this.#push(compressed);

    this.entries.push({
      nameBuf,
      crc,
      method,
      compressedSize: compressed.length,
      size: body.length,
      localOffset,
    });
  }

  toBuffer() {
    const cdStart = this.offset;
    for (const e of this.entries) {
      const h = Buffer.alloc(46);
      h.writeUInt32LE(0x02014b50, 0);
      h.writeUInt16LE(0x031e, 4); // версия создателя (unix)
      h.writeUInt16LE(20, 6);
      h.writeUInt16LE(0x0800, 8);
      h.writeUInt16LE(e.method, 10);
      h.writeUInt16LE(DOS_TIME, 12);
      h.writeUInt16LE(DOS_DATE, 14);
      h.writeUInt32LE(e.crc, 16);
      h.writeUInt32LE(e.compressedSize, 20);
      h.writeUInt32LE(e.size, 24);
      h.writeUInt16LE(e.nameBuf.length, 28);
      h.writeUInt16LE(0, 30); // extra
      h.writeUInt16LE(0, 32); // comment
      h.writeUInt16LE(0, 34); // disk
      h.writeUInt16LE(0, 36); // internal attrs
      h.writeUInt32LE(0o644 << 16, 38); // external attrs
      h.writeUInt32LE(e.localOffset, 42);
      this.#push(h);
      this.#push(e.nameBuf);
    }
    const cdSize = this.offset - cdStart;

    const end = Buffer.alloc(22);
    end.writeUInt32LE(0x06054b50, 0);
    end.writeUInt16LE(0, 4);
    end.writeUInt16LE(0, 6);
    end.writeUInt16LE(this.entries.length, 8);
    end.writeUInt16LE(this.entries.length, 10);
    end.writeUInt32LE(cdSize, 12);
    end.writeUInt32LE(cdStart, 16);
    end.writeUInt16LE(0, 20);
    this.#push(end);

    return Buffer.concat(this.parts);
  }
}
