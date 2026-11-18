'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const {
  BriefingPlayer,
  fmt,
  runtime,
  sortBriefings,
  buildVariants,
  groupIntoStories,
  generationSlot,
  editionDate
} = require('./player.js');

class FakeClassList {
  constructor(element) { this.element = element; }
  toggle(name, force) {
    const enabled = force === undefined ? !this.element._classes.has(name) : Boolean(force);
    if (enabled) this.element._classes.add(name);
    else this.element._classes.delete(name);
    return enabled;
  }
  contains(name) { return this.element._classes.has(name); }
}

class FakeElement {
  constructor(tagName) {
    this.tagName = String(tagName).toUpperCase();
    this.children = [];
    this.parentNode = null;
    this.dataset = {};
    this.attributes = new Map();
    this.listeners = new Map();
    this._classes = new Set();
    this.classList = new FakeClassList(this);
    this._textContent = '';
    this.hidden = false;
    this.disabled = false;
    this.value = '';
    this.isContentEditable = false;
  }
  set className(value) { this._classes = new Set(String(value).split(/\s+/).filter(Boolean)); }
  get className() { return Array.from(this._classes).join(' '); }
  set textContent(value) { this.children = []; this._textContent = String(value); }
  get textContent() { return this.children.length ? this.children.map(child => child.textContent).join('') : this._textContent; }
  get firstChild() { return this.children[0] || null; }
  get options() { return this.children.filter(child => child.tagName === 'OPTION'); }
  appendChild(child) { child.parentNode = this; this.children.push(child); return child; }
  replaceChildren(...children) { this.children = []; this._textContent = ''; children.forEach(child => this.appendChild(child)); }
  setAttribute(name, value) {
    const text = String(value);
    this.attributes.set(name, text);
    if (name === 'class') this.className = text;
    if (name === 'href') this.href = text;
  }
  removeAttribute(name) { this.attributes.delete(name); }
  addEventListener(type, callback) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(callback);
  }
  emit(type, extra) {
    const event = Object.assign({ type, target: this, preventDefault() {} }, extra);
    (this.listeners.get(type) || []).forEach(callback => callback(event));
  }
  querySelectorAll(selector) {
    const className = selector.startsWith('.') ? selector.slice(1) : null;
    const tagName = className ? null : selector.toUpperCase();
    const matches = [];
    const visit = element => {
      element.children.forEach(child => {
        if (child instanceof FakeElement) {
          if ((className && child.classList.contains(className)) || (tagName && child.tagName === tagName)) matches.push(child);
          visit(child);
        }
      });
    };
    visit(this);
    return matches;
  }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
}

class FakeAudio extends FakeElement {
  constructor() {
    super('audio');
    this.paused = true;
    this.currentTime = 0;
    this.duration = NaN;
    this.volume = 1;
    this.src = '';
    this.playCalls = 0;
  }
  play() { this.paused = false; this.playCalls++; this.emit('play'); return Promise.resolve(); }
  pause() { if (!this.paused) { this.paused = true; this.emit('pause'); } }
  load() {}
  removeAttribute(name) { super.removeAttribute(name); if (name === 'src') this.src = ''; }
}

class FakeDocument {
  constructor(ids) {
    this.elements = new Map();
    this.listeners = new Map();
    ids.forEach(id => {
      const tag = id === 'briefing-select' ? 'select' : (id === 'audio' ? 'audio' : 'div');
      const element = tag === 'audio' ? new FakeAudio() : new FakeElement(tag);
      if (id === 'volume') element.value = '1';
      this.elements.set(id, element);
    });
    this.defaultView = { setTimeout, localStorage: new MemoryStorage(), fetch: async () => { throw new Error('No fetch mock'); } };
  }
  getElementById(id) { return this.elements.get(id) || null; }
  createElement(tag) { return new FakeElement(tag); }
  createElementNS(namespace, tag) { return new FakeElement(tag); }
  createTextNode(text) { const node = new FakeElement('#text'); node.textContent = text; return node; }
  addEventListener(type, callback) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(callback);
  }
  keydown(event) { (this.listeners.get('keydown') || []).forEach(callback => callback(event)); }
}

class MemoryStorage {
  constructor(initial) { this.values = new Map(Object.entries(initial || {})); }
  getItem(key) { return this.values.has(key) ? this.values.get(key) : null; }
  setItem(key, value) { this.values.set(key, String(value)); }
}

const ids = [
  'audio', 'playlist', 'briefing-select', 'now-playing', 'now-subtitle', 'now-type',
  'play', 'play-icon', 'prev', 'next', 'seek', 'volume', 'time', 'total',
  'genre-bar', 'hero-play', 'hero-play-label',
  'hero-play-icon', 'briefing-kicker', 'briefing-date', 'briefing-slot', 'story-count',
  'briefing-duration', 'active-genre-stat', 'session-genre', 'session-channel',
  'session-count', 'session-edition', 'up-next-icon', 'up-next-title', 'up-next-subtitle',
  'notice', 'notice-text', 'empty-state', 'empty-title', 'empty-message', 'empty-action', 'player'
];

function playlist(date, slot) {
  const news = index => ({
    id: 'n' + index,
    type: 'news',
    title: 'Story ' + index,
    subtitle: index ? 'Technology' : 'World',
    url: '/audio/news' + index + '.mp3',
    duration: 60,
    sources: Array.from({ length: index ? 1 : 12 }, (_, source) => ({
      newspaper: 'Paper ' + source,
      title: 'Source ' + source,
      url: 'https://example.com/' + source
    }))
  });
  const items = [
    news(0),
    { id: 'i0', type: 'intro', title: 'Song A', subtitle: 'Artist A', url: '/audio/intro0.mp3', duration: 5 },
    { id: 'm0', type: 'music', title: 'Song A', subtitle: 'Artist A', url: '/audio/music0.mp3', duration: 200 },
    news(1),
    { id: 'i1', type: 'intro', title: 'Song B', subtitle: 'Artist B', url: '/audio/intro1.mp3', duration: 5 },
    { id: 'm1', type: 'music', title: 'Song B', subtitle: 'Artist B', url: '/audio/music1.mp3', duration: 180 }
  ];
  const jazz = [items[0], { ...items[2], id: 'j0', title: 'Jazz A', url: '/audio/jazz0.mp3' }, items[3], { ...items[5], id: 'j1', title: 'Jazz B', url: '/audio/jazz1.mp3' }];
  return {
    date: date || '2026-11-13',
    slot: slot || 'morning',
    created: (date || '2026-11-13') + 'T08:00:00+02:00',
    default: 'mix',
    genres: [{ id: 'mix', label: 'Mix' }, { id: 'jazz', label: 'Jazz' }],
    items,
    variants: { jazz }
  };
}

function response(body, status) {
  const code = status || 200;
  return { ok: code >= 200 && code < 300, status: code, json: async () => body };
}

function fixture(overrides, saved) {
  const doc = new FakeDocument(ids);
  const calls = [];
  const current = playlist();
  const routes = Object.assign({
    '/api/briefings': [
      { date: '2026-11-12', slot: 'evening', path: '2026-11-12/evening' },
      { date: '2026-11-13', slot: 'morning', path: '2026-11-13/morning' },
      { date: '2026-11-13', slot: 'evening', path: '2026-11-13/evening' }
    ],
    '/api/latest': current,
    '/api/status': { state: 'idle', current_slot: '', last_error: '' }
  }, overrides);
  const fetcher = async (url, options) => {
    calls.push({ url, options });
    const route = routes[url];
    if (typeof route === 'function') return route(url, options);
    if (route && route.__response) return response(route.body, route.status);
    if (route !== undefined) return response(route);
    return response({ error: 'not found' }, 404);
  };
  const storage = new MemoryStorage(saved ? { briefing: JSON.stringify(saved) } : {});
  const player = new BriefingPlayer(doc, { fetch: fetcher, storage, sleep: async () => {} });
  return { player, doc, audio: doc.getElementById('audio'), calls, routes, storage };
}

test('formatting, sorting, slots and edition dates remain compatible', () => {
  assert.equal(fmt(65.9), '1:05');
  assert.equal(fmt(-1), '0:00');
  assert.equal(fmt(Infinity), '0:00');
  assert.equal(runtime(2700), '45 min');
  assert.equal(runtime(5400), '1h 30m');
  assert.deepEqual(sortBriefings(null), []);
  assert.deepEqual(sortBriefings([
    { date: '2026-01-01', slot: 'morning' },
    { date: '2026-01-02', slot: 'morning' },
    { date: '2026-01-02', slot: 'evening' }
  ]).map(item => item.date + '/' + item.slot), ['2026-01-02/evening', '2026-01-02/morning', '2026-01-01/morning']);
  assert.equal(generationSlot('2026-01-01/evening', 8), 'evening');
  assert.equal(generationSlot('2026-01-01/morning', 20), 'morning');
  assert.equal(generationSlot('', 18), 'evening');
  assert.equal(generationSlot('', 8), 'morning');
  assert.match(editionDate('2026-11-13'), /Friday 13 November/);
});

test('variant construction supports current and legacy playlists', () => {
  const current = buildVariants(playlist());
  assert.deepEqual(current.genreOrder, ['mix', 'jazz']);
  assert.equal(current.variants.mix.length, 6);
  assert.equal(current.variants.jazz.length, 4);
  const legacy = buildVariants({ items: [{ type: 'news', title: 'Legacy' }] });
  assert.deepEqual(legacy.genreOrder, ['mix']);
  assert.equal(legacy.variants.mix.length, 1);
  const emptyChannel = buildVariants({ default: 'mix', items: [], genres: [{ id: 'rock' }], variants: {} });
  assert.deepEqual(emptyChannel.variants.rock, []);
});

test('story grouping preserves news-intro-music order and orphan tracks', () => {
  const groups = groupIntoStories(playlist().items);
  assert.equal(groups.length, 2);
  assert.equal(groups[0].newsIndex, 0);
  assert.deepEqual(groups[0].tracks.map(track => track.index), [1, 2]);
  const orphan = groupIntoStories([{ type: 'music', title: 'Opening' }]);
  assert.equal(orphan[0].news, null);
  assert.equal(orphan[0].tracks[0].index, 0);
});

test('initialization loads sorted archive, latest playlist, channels and metadata', async () => {
  const { player, doc } = fixture();
  await player.init();
  assert.equal(doc.getElementById('playlist').children.length, 2);
  assert.equal(doc.getElementById('genre-bar').children.length, 2);
  assert.deepEqual(doc.getElementById('briefing-select').options.map(option => option.value), [
    '2026-11-13/evening', '2026-11-13/morning', '2026-11-12/evening'
  ]);
  assert.equal(doc.getElementById('briefing-select').value, '2026-11-13/morning');
  assert.equal(doc.getElementById('story-count').textContent, '02');
  assert.equal(doc.getElementById('briefing-duration').textContent, '9 min');
  assert.equal(doc.getElementById('session-count').textContent, '6');
  assert.equal(doc.getElementById('empty-state').hidden, true);
  assert.equal(doc.getElementById('play').disabled, false);
  assert.equal(doc.getElementById('now-playing').textContent, 'Ready when you are');
});

test('story and track controls play, pause, advance, restart, seek and set volume', async () => {
  const { player, doc, audio } = fixture();
  await player.init();
  const firstCard = doc.getElementById('playlist').children[0];
  firstCard.querySelector('.story-head').emit('click');
  assert.equal(player.index, 0);
  assert.equal(audio.src, '/audio/news0.mp3');
  assert.equal(audio.paused, false);
  assert.equal(doc.getElementById('now-playing').textContent, 'Story 0');
  assert.equal(doc.getElementById('play-icon').attributes.get('href'), '#icon-pause');

  doc.getElementById('play').emit('click');
  assert.equal(audio.paused, true);
  doc.getElementById('next').emit('click');
  assert.equal(player.index, 1);
  assert.equal(audio.src, '/audio/intro0.mp3');
  audio.currentTime = 8;
  player.previous();
  assert.equal(player.index, 1);
  assert.equal(audio.currentTime, 0);
  audio.currentTime = 3;
  player.previous();
  assert.equal(player.index, 0);

  audio.duration = 100;
  doc.getElementById('seek').value = '50';
  doc.getElementById('seek').emit('input');
  assert.equal(audio.currentTime, 50);
  doc.getElementById('volume').value = '0.35';
  doc.getElementById('volume').emit('input');
  assert.equal(audio.volume, 0.35);

  const musicRow = firstCard.querySelectorAll('.track-row')[1];
  musicRow.emit('click');
  assert.equal(player.index, 2);
  assert.equal(musicRow.classList.contains('active'), true);
  assert.equal(firstCard.classList.contains('active'), true);
  audio.emit('ended');
  assert.equal(player.index, 3);
  audio.emit('error');
  assert.equal(player.index, 4);
});

test('all sources expand accessibly and open safely in a new tab', async () => {
  const { player, doc } = fixture();
  await player.init();
  const firstCard = doc.getElementById('playlist').children[0];
  const toggle = firstCard.querySelector('.src-toggle');
  const sources = firstCard.querySelector('.story-sources');
  assert.equal(sources.hidden, true);
  assert.equal(toggle.attributes.get('aria-expanded'), 'false');
  toggle.emit('click');
  assert.equal(sources.hidden, false);
  assert.equal(toggle.attributes.get('aria-expanded'), 'true');
  assert.equal(sources.querySelectorAll('li').length, 12);
  const link = sources.querySelector('a');
  assert.equal(link.target, '_blank');
  assert.equal(link.rel, 'noopener noreferrer');
  toggle.emit('click');
  assert.equal(sources.hidden, true);
});

test('same-briefing state restores channel, item and offset without autoplay', async () => {
  const { player, audio } = fixture({}, { id: '2026-11-13/morning', index: 1, time: 42, genre: 'jazz' });
  await player.init();
  assert.equal(player.activeGenre, 'jazz');
  assert.equal(player.items.length, 4);
  assert.equal(player.index, 1);
  assert.equal(audio.src, '/audio/jazz0.mp3');
  assert.equal(audio.currentTime, 42);
  assert.equal(audio.paused, true);
  assert.equal(audio.playCalls, 0);
  audio.duration = 200;
  audio.currentTime = 50;
  audio.emit('timeupdate');
  const saved = JSON.parse(player.storage.getItem('briefing'));
  assert.deepEqual(saved, { id: '2026-11-13/morning', index: 1, time: 50, genre: 'jazz' });
});

test('switching channels preserves active news but resets active music', async () => {
  const { player, audio } = fixture();
  await player.init();
  player.play(0);
  const newsURL = audio.src;
  player.selectGenre('jazz');
  assert.equal(player.activeGenre, 'jazz');
  assert.equal(player.index, 0);
  assert.equal(audio.src, newsURL);
  assert.equal(audio.paused, false);
  assert.equal(player.items[1].title, 'Jazz A');

  player.play(1);
  player.selectGenre('mix');
  assert.equal(player.activeGenre, 'mix');
  assert.equal(player.index, -1);
  assert.equal(audio.src, '');
  assert.equal(audio.paused, true);
  assert.equal(player.items.length, 6);
});

test('historical selection requests the selected date and slot', async () => {
  const historical = playlist('2026-11-12', 'evening');
  const { player, doc, calls } = fixture({ '/api/briefing?date=2026-11-12&slot=evening': historical });
  await player.init();
  doc.getElementById('briefing-select').value = '2026-11-12/evening';
  doc.getElementById('briefing-select').emit('change');
  await new Promise(resolve => setImmediate(resolve));
  assert.ok(calls.some(call => call.url === '/api/briefing?date=2026-11-12&slot=evening'));
  assert.equal(player.briefingId, '2026-11-12/evening');
  assert.equal(player.playlist.slot, 'evening');
  assert.equal(doc.getElementById('session-edition').textContent, 'Evening');
});

test('manual generation keeps CSRF marker, polls and reloads the new edition', async () => {
  let statusChecks = 0;
  let generateOptions;
  const { player, doc, calls } = fixture({
    '/api/generate?slot=evening': (url, options) => { generateOptions = options; return response({ status: 'started' }, 202); },
    '/api/status': () => {
      const check = ++statusChecks;
      return response(check === 1 ? { state: 'idle' } : (check === 2 ? { state: 'running' } : { state: 'idle', last_error: '' }));
    }
  });
  await player.init();
  doc.getElementById('briefing-select').value = '2026-11-13/evening';
  await player.generate();
  assert.equal(generateOptions.method, 'POST');
  assert.equal(generateOptions.headers['X-Requested-With'], 'fetch');
  assert.ok(calls.filter(call => call.url === '/api/latest').length >= 2);
  assert.equal(doc.getElementById('empty-action').disabled, false);
  assert.match(doc.getElementById('notice-text').textContent, /new edition is ready/);
});

test('HTTP 409 generation response is treated as busy and polled', async () => {
  let statusChecks = 0;
  const { player, calls } = fixture({
    '/api/generate?slot=morning': () => response({ error: 'generation already running' }, 409),
    '/api/status': () => {
      const check = ++statusChecks;
      return response(check === 1 ? { state: 'idle' } : (check === 2 ? { state: 'running' } : { state: 'idle', last_error: '' }));
    }
  });
  await player.init();
  await player.generate();
  assert.ok(calls.some(call => call.url === '/api/generate?slot=morning'));
  assert.ok(statusChecks >= 2);
  assert.equal(player.polling, false);
});

test('generation request failures are surfaced without disabling the player forever', async () => {
  const { player, doc } = fixture({
    '/api/generate?slot=morning': () => response({ error: 'upstream unavailable' }, 500)
  });
  await player.init();
  await player.generate();
  assert.equal(doc.getElementById('empty-action').disabled, false);
  assert.equal(doc.getElementById('notice-text').textContent, 'upstream unavailable');
  assert.equal(doc.getElementById('notice').classList.contains('is-error'), true);
});

test('an already-running generation is detected during initialization', async () => {
  let checks = 0;
  const { player } = fixture({
    '/api/status': () => response(++checks === 1 ? { state: 'running', current_slot: 'morning' } : { state: 'idle', last_error: '' })
  });
  await player.init();
  await new Promise(resolve => setImmediate(resolve));
  assert.ok(checks >= 2);
  assert.equal(player.polling, false);
});

test('keyboard shortcuts work and ignore form fields and modified keys', async () => {
  const { player, doc, audio } = fixture();
  await player.init();
  const bodyTarget = new FakeElement('body');
  doc.keydown({ key: ' ', target: bodyTarget, preventDefault() {} });
  assert.equal(player.index, 0);
  assert.equal(audio.paused, false);
  audio.currentTime = 20;
  doc.keydown({ key: 'ArrowLeft', target: bodyTarget });
  assert.equal(audio.currentTime, 15);
  audio.duration = 100;
  doc.keydown({ key: 'ArrowRight', target: bodyTarget });
  assert.equal(audio.currentTime, 20);
  doc.keydown({ key: 'N', target: bodyTarget });
  assert.equal(player.index, 1);
  audio.currentTime = 0;
  doc.keydown({ key: 'P', target: bodyTarget });
  assert.equal(player.index, 0);
  doc.keydown({ key: 'N', target: new FakeElement('input') });
  assert.equal(player.index, 0);
  doc.keydown({ key: 'N', target: bodyTarget, ctrlKey: true });
  assert.equal(player.index, 0);
});

test('terminal generation and audio errors are shown clearly', async () => {
  let checks = 0;
  const { player, doc, audio } = fixture({
    '/api/generate?slot=morning': () => response({ status: 'started' }, 202),
    '/api/status': () => response(++checks === 1 ? { state: 'idle' } : { state: 'error', last_error: 'TTS unavailable' })
  });
  await player.init();
  await player.generate();
  assert.equal(doc.getElementById('notice-text').textContent, 'Generation failed: TTS unavailable');
  assert.equal(doc.getElementById('notice').classList.contains('is-error'), true);
  player.play(player.items.length - 1);
  audio.emit('error');
  assert.equal(doc.getElementById('notice-text').textContent, 'This audio clip could not be played.');
  assert.equal(doc.getElementById('play-icon').attributes.get('href'), '#icon-play');
});

test('empty genre channels reset cleanly and can be changed back', async () => {
  const current = playlist();
  current.genres.push({ id: 'rock', label: 'Rock' });
  current.variants.rock = [];
  const { player, doc } = fixture({ '/api/latest': current });
  await player.init();
  player.play(2);
  player.selectGenre('rock');
  assert.equal(player.items.length, 0);
  assert.equal(player.index, -1);
  assert.equal(doc.getElementById('empty-state').hidden, false);
  assert.match(doc.getElementById('notice-text').textContent, /No tracks/);
  player.selectGenre('mix');
  assert.equal(player.items.length, 6);
  assert.equal(doc.getElementById('empty-state').hidden, true);
});

test('saved playback state is never applied to a different briefing', async () => {
  const { player, audio } = fixture({}, { id: '2026-01-01/morning', index: 3, time: 30, genre: 'jazz' });
  await player.init();
  assert.equal(player.activeGenre, 'mix');
  assert.equal(player.index, -1);
  assert.equal(audio.src, '');
  assert.equal(audio.playCalls, 0);
});

test('empty and failed latest responses render usable empty states', async () => {
  const empty = fixture({ '/api/latest': { __response: true, body: { error: 'no briefing generated yet' }, status: 404 }, '/api/briefings': null });
  await empty.player.init();
  assert.equal(empty.player.items.length, 0);
  assert.equal(empty.doc.getElementById('empty-state').hidden, false);
  assert.equal(empty.doc.getElementById('play').disabled, true);
  assert.equal(empty.doc.getElementById('briefing-select').options[0].textContent, '— No editions yet —');

  const failed = fixture({ '/api/latest': { __response: true, body: { error: 'corrupt playlist' }, status: 500 } });
  await failed.player.init();
  assert.equal(failed.doc.getElementById('notice-text').textContent, 'The briefing could not be loaded. Please try again.');
  assert.equal(failed.doc.getElementById('empty-state').hidden, false);
});
