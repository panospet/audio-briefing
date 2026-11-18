(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  if (root && root.document) {
    const start = () => {
      const player = new api.BriefingPlayer(root.document);
      player.init();
    };
    if (root.document.readyState === 'loading') root.document.addEventListener('DOMContentLoaded', start, { once: true });
    else start();
  }
})(typeof window !== 'undefined' ? window : globalThis, function () {
  'use strict';

  const TYPE_LABELS = { news: 'NEWS BRIEFING', intro: 'SPOKEN INTRO', music: 'MUSIC DISCOVERY' };
  const TYPE_ICONS = { news: 'news', intro: 'intro', music: 'music' };

  function fmt(seconds) {
    let value = Number(seconds);
    if (!Number.isFinite(value) || value < 0) value = 0;
    const minutes = Math.floor(value / 60);
    const secs = Math.floor(value % 60);
    return minutes + ':' + String(secs).padStart(2, '0');
  }

  function runtime(seconds) {
    const minutes = Math.max(0, Math.round((Number(seconds) || 0) / 60));
    if (minutes < 60) return minutes + ' min';
    const hours = Math.floor(minutes / 60);
    const rest = minutes % 60;
    return hours + 'h' + (rest ? ' ' + rest + 'm' : '');
  }

  function slotRank(slot) { return slot === 'evening' ? 2 : 1; }

  function sortBriefings(list) {
    return (Array.isArray(list) ? list : []).slice().sort((a, b) => {
      if (a.date !== b.date) return String(b.date).localeCompare(String(a.date));
      return slotRank(b.slot) - slotRank(a.slot);
    });
  }

  function buildVariants(playlist) {
    const variants = {};
    const genreOrder = [];
    const genreLabels = {};
    const def = playlist.default || 'mix';
    variants[def] = Array.isArray(playlist.items) ? playlist.items : [];
    genreLabels[def] = def === 'mix' ? 'Mix' : def;
    (Array.isArray(playlist.genres) ? playlist.genres : []).forEach(genre => {
      if (!genre || !genre.id) return;
      genreLabels[genre.id] = genre.label || genre.id;
      variants[genre.id] = genre.id === def
        ? variants[def]
        : ((playlist.variants && Array.isArray(playlist.variants[genre.id])) ? playlist.variants[genre.id] : []);
      if (!genreOrder.includes(genre.id)) genreOrder.push(genre.id);
    });
    if (!genreOrder.includes(def)) genreOrder.unshift(def);
    return { variants, genreOrder, genreLabels };
  }

  function groupIntoStories(items) {
    const stories = [];
    let current = null;
    (Array.isArray(items) ? items : []).forEach((item, index) => {
      if (item.type === 'news') {
        if (current) stories.push(current);
        current = { news: item, newsIndex: index, tracks: [] };
      } else {
        if (!current) current = { news: null, newsIndex: -1, tracks: [] };
        current.tracks.push({ item, index });
      }
    });
    if (current) stories.push(current);
    return stories;
  }

  function generationSlot(selectedValue, hour) {
    if (selectedValue && selectedValue.includes('/evening')) return 'evening';
    if (!selectedValue && Number(hour) >= 18) return 'evening';
    return 'morning';
  }

  function editionDate(date, created) {
    const raw = date || (created ? String(created).slice(0, 10) : '');
    if (!/^\d{4}-\d{2}-\d{2}$/.test(raw)) return 'Your latest audio edition';
    const parsed = new Date(raw + 'T12:00:00');
    if (Number.isNaN(parsed.getTime())) return raw;
    return new Intl.DateTimeFormat('en-GB', { weekday: 'long', day: 'numeric', month: 'long' }).format(parsed);
  }

  class BriefingPlayer {
    constructor(doc, options) {
      this.doc = doc;
      this.options = options || {};
      this.win = doc.defaultView || (typeof window !== 'undefined' ? window : globalThis);
      this.fetch = this.options.fetch || this.win.fetch.bind(this.win);
      this.sleep = this.options.sleep || (ms => new Promise(resolve => this.win.setTimeout(resolve, ms)));
      this.storage = this.options.storage || this.win.localStorage;
      const ids = [
        'audio', 'playlist', 'briefing-select', 'now-playing', 'now-subtitle', 'now-type',
        'play', 'play-icon', 'prev', 'next', 'seek', 'volume', 'time', 'total',
        'genre-bar', 'hero-play', 'hero-play-label',
        'hero-play-icon', 'briefing-kicker', 'briefing-date', 'briefing-slot', 'story-count',
        'briefing-duration', 'active-genre-stat', 'session-genre', 'session-channel',
        'session-count', 'session-edition', 'up-next-icon', 'up-next-title', 'up-next-subtitle',
        'notice', 'notice-text', 'empty-state', 'empty-title', 'empty-message', 'empty-action', 'player'
      ];
      this.el = {};
      ids.forEach(id => { this.el[id] = doc.getElementById(id); });
      this.audio = this.el.audio;
      this.podcastMode = this.detectPodcastMode();
      this.items = [];
      this.index = -1;
      this.briefingId = '';
      this.playlist = null;
      this.polling = false;
      this.variants = {};
      this.genreOrder = [];
      this.genreLabels = {};
      this.activeGenre = null;
      this.requestSerial = 0;
      this.pendingResumeTime = null;
    }

    node(tag, className, text) {
      const element = this.doc.createElement(tag);
      if (className) element.className = className;
      if (text !== undefined) element.textContent = text;
      return element;
    }

    detectPodcastMode() {
      // /podcast serves the same SPA; only the path distinguishes the edition.
      try {
        const location = this.win.location;
        return !!(location && /^\/podcast\/?$/.test(location.pathname || ''));
      } catch (error) {
        return false;
      }
    }

    applyModeChrome() {
      // Header link: PODCAST on the briefing page, BRIEFINGS on the podcast page.
      const link = this.el['podcast-link'];
      if (link) {
        if (this.podcastMode) {
          link.href = '/';
          link.setAttribute('aria-label', 'Daily briefings');
          const label = link.querySelector('span');
          if (label) label.textContent = 'BRIEFINGS';
        }
      }
      if (!this.podcastMode) return;
      const picker = this.doc.querySelector('.archive-picker');
      if (picker) picker.hidden = true;
      const heroTitle = this.doc.getElementById('hero-title');
      if (heroTitle) heroTitle.innerHTML = 'Nina Simone.<br><em>A Buyer\u2019s Guide.</em>';
      const heroCopy = this.doc.querySelector('.hero-copy');
      if (heroCopy) heroCopy.textContent = 'Ten albums, told and played in full \u2014 the rocking.gr buyer\u2019s guide turned into one long listening session.';
      const heroCaption = this.doc.querySelector('.hero-art-caption');
      if (heroCaption && heroCaption.firstChild) heroCaption.firstChild.textContent = 'SPECIAL EDITION';
    }

    icon(name) {
      const svg = this.doc.createElementNS('http://www.w3.org/2000/svg', 'svg');
      svg.setAttribute('class', 'icon');
      svg.setAttribute('aria-hidden', 'true');
      const use = this.doc.createElementNS('http://www.w3.org/2000/svg', 'use');
      use.setAttribute('href', '#icon-' + (TYPE_ICONS[name] || name));
      svg.appendChild(use);
      return svg;
    }

    init() {
      this.audio.addEventListener('play', () => this.setPlayingUI(true));
      this.audio.addEventListener('pause', () => this.setPlayingUI(false));
      this.audio.addEventListener('ended', () => this.onEnded());
      this.audio.addEventListener('error', () => this.onAudioError());
      this.audio.addEventListener('timeupdate', () => this.onTimeUpdate());
      this.audio.addEventListener('loadedmetadata', () => this.onLoadedMetadata());

      this.el.play.addEventListener('click', () => this.togglePlay());
      this.el['hero-play'].addEventListener('click', () => this.togglePlay());
      this.el.prev.addEventListener('click', () => this.previous());
      this.el.next.addEventListener('click', () => this.next());
      this.el.seek.addEventListener('input', () => this.seek());
      this.el.volume.addEventListener('input', () => { this.audio.volume = Number(this.el.volume.value); });
      this.el['briefing-select'].addEventListener('change', () => this.onBriefingSelected());
      this.el['empty-action'].addEventListener('click', () => this.generate());
      this.doc.addEventListener('keydown', event => this.onKeydown(event));

      this.audio.volume = Number(this.el.volume.value);
      this.applyModeChrome();
      this.renderGenreBar();
      this.render();
      this.setPlayingUI(false);
      const initialUrl = this.podcastMode ? '/api/podcast' : '/api/latest';
      const initialId = this.podcastMode ? 'podcast' : '';
      const editions = this.loadBriefings().then(() => this.loadPlaylist(initialUrl, initialId));
      const status = this.checkInitialStatus();
      return Promise.all([editions, status]);
    }

    async request(url, options) {
      const response = await this.fetch(url, options);
      if (!response.ok) {
        let message = 'Request failed';
        try {
          const body = await response.json();
          if (body && body.error) message = body.error;
        } catch (error) { /* The status code is enough when no JSON body exists. */ }
        const failure = new Error(message);
        failure.status = response.status;
        throw failure;
      }
      return response.json();
    }

    async loadBriefings() {
      try {
        const list = sortBriefings(await this.request('/api/briefings'));
        const select = this.el['briefing-select'];
        select.replaceChildren();
        if (!list.length) {
          const option = this.node('option', '', '— No editions yet —');
          option.value = '';
          select.appendChild(option);
          return list;
        }
        list.forEach(briefing => {
          const option = this.node('option');
          option.value = briefing.date + '/' + briefing.slot;
          option.textContent = briefing.date + ' · ' + (briefing.slot === 'evening' ? 'Evening' : 'Morning');
          select.appendChild(option);
        });
        this.syncSelect();
        return list;
      } catch (error) {
        const select = this.el['briefing-select'];
        select.replaceChildren();
        const option = this.node('option', '', 'Archive unavailable');
        option.value = '';
        select.appendChild(option);
        return [];
      }
    }

    syncSelect() {
      if (!this.briefingId) return;
      const select = this.el['briefing-select'];
      for (const option of select.options) {
        if (option.value === this.briefingId) {
          select.value = this.briefingId;
          break;
        }
      }
    }

    onBriefingSelected() {
      const value = this.el['briefing-select'].value;
      if (!value) return;
      const parts = value.split('/');
      const date = parts[0];
      const slot = parts[1];
      this.loadPlaylist('/api/briefing?date=' + encodeURIComponent(date) + '&slot=' + encodeURIComponent(slot), value);
    }

    resetPlayerState() {
      this.audio.pause();
      this.audio.removeAttribute('src');
      if (typeof this.audio.load === 'function') this.audio.load();
      this.index = -1;
      this.pendingResumeTime = null;
      this.el.seek.value = 0;
      this.el.time.textContent = '0:00';
      this.el.total.textContent = '0:00';
      this.setPlayingUI(false);
      this.setNowPlaying(null);
    }

    async loadPlaylist(url, id) {
      const serial = ++this.requestSerial;
      this.resetPlayerState();
      if (!this.polling) this.clearNotice();
      try {
        const playlist = await this.request(url);
        if (serial !== this.requestSerial) return;
        this.playlist = playlist;
        this.briefingId = id || (playlist.date && playlist.slot
          ? playlist.date + '/' + playlist.slot
          : (playlist.created || ''));
        const built = buildVariants(playlist);
        this.variants = built.variants;
        this.genreOrder = built.genreOrder;
        this.genreLabels = built.genreLabels;
        this.activeGenre = this.chooseGenre();
        this.items = this.variants[this.activeGenre] || [];
        this.syncSelect();
        this.renderGenreBar();
        this.render();
        this.resume();
      } catch (error) {
        if (serial !== this.requestSerial) return;
        this.playlist = null;
        this.briefingId = '';
        this.variants = {};
        this.genreOrder = [];
        this.genreLabels = {};
        this.activeGenre = null;
        this.items = [];
        this.renderGenreBar();
        this.render();
        this.el['empty-title'].textContent = 'Your next edition is waiting.';
        this.el['empty-message'].textContent = 'No briefing is available yet. Generate one to fill the airwaves.';
        if (this.podcastMode) {
          this.el['empty-title'].textContent = 'No podcast published yet.';
          this.el['empty-message'].textContent = 'Publish a podcast edition into the storage root and it will appear here.';
        }
        if (error.status !== 404) this.showNotice('The briefing could not be loaded. Please try again.', 'error');
      }
    }

    chooseGenre() {
      const saved = this.readSaved();
      if (saved && saved.id === this.briefingId && saved.genre && Object.prototype.hasOwnProperty.call(this.variants, saved.genre)) return saved.genre;
      return this.genreOrder[0] || 'mix';
    }

    renderGenreBar() {
      const bar = this.el['genre-bar'];
      bar.replaceChildren();
      bar.hidden = this.genreOrder.length <= 1;
      this.genreOrder.forEach(id => {
        const button = this.node('button', 'genre-btn' + (id === this.activeGenre ? ' active' : ''), this.genreLabels[id] || id);
        button.type = 'button';
        button.setAttribute('aria-pressed', id === this.activeGenre ? 'true' : 'false');
        button.addEventListener('click', () => this.selectGenre(id));
        bar.appendChild(button);
      });
    }

    selectGenre(id) {
      if (!Object.prototype.hasOwnProperty.call(this.variants, id) || id === this.activeGenre) return;
      const nextItems = this.variants[id] || [];
      const current = this.index >= 0 && this.index < this.items.length ? this.items[this.index] : null;

      // News is shared between variants. Keep narration playing and swap only
      // the following intro/music items when a channel changes mid-story.
      if (current && current.type === 'news' && this.index < nextItems.length) {
        this.activeGenre = id;
        this.items = nextItems;
        this.renderGenreBar();
        this.render();
        this.save();
        return;
      }

      this.activeGenre = id;
      this.resetPlayerState();
      this.items = nextItems;
      this.renderGenreBar();
      this.render();
      if (!nextItems.length) this.showNotice('No tracks are available in this channel yet.');
      else this.clearNotice();
      this.save();
    }

    render() {
      const playlist = this.el.playlist;
      playlist.replaceChildren();
      this.updateMetadata();
      this.el['empty-state'].hidden = this.items.length > 0;
      if (!this.items.length) {
        this.setNowPlaying(null);
        this.updateUpNext();
        this.setPlayingUI(false);
        return;
      }

      groupIntoStories(this.items).forEach((story, storyIndex) => {
        const card = this.node('li', 'story-card');
        card.dataset.story = String(storyIndex);
        if (story.newsIndex >= 0) {
          card.dataset.first = String(story.newsIndex);
          card.dataset.last = String(story.tracks.length ? story.tracks[story.tracks.length - 1].index : story.newsIndex);
        }

        const head = this.node('button', 'story-head');
        head.type = 'button';
        head.setAttribute('aria-label', 'Play ' + (story.news ? story.news.title : 'audio segment'));
        const number = this.node('span', 'story-number', String(storyIndex + 1).padStart(2, '0'));
        const meta = this.node('span', 'story-meta');
        const category = story.news && story.news.subtitle ? story.news.subtitle : 'Daily briefing';
        meta.appendChild(this.node('span', 'story-category', category));
        meta.appendChild(this.node('span', 'story-title', story.news ? story.news.title : 'Audio interlude'));
        const sub = this.node('span', 'story-sub');
        if (story.news) {
          const duration = this.node('span');
          duration.appendChild(this.icon('clock'));
          duration.appendChild(this.doc.createTextNode(fmt(story.news.duration)));
          sub.appendChild(duration);
          if (story.news.sources && story.news.sources.length) sub.appendChild(this.node('span', '', story.news.sources.length + ' sources'));
        }
        meta.appendChild(sub);
        const playMark = this.node('span', 'story-play');
        playMark.appendChild(this.icon('play'));
        head.appendChild(number);
        head.appendChild(meta);
        head.appendChild(playMark);
        head.addEventListener('click', () => { if (story.newsIndex >= 0) this.play(story.newsIndex); });
        card.appendChild(head);

        if (story.newsIndex >= 0 || story.tracks.length) {
          const tail = this.node('div', 'story-tail');
          if (story.news && story.news.sources && story.news.sources.length) {
            const actions = this.node('div', 'story-actions');
            const toggle = this.node('button', 'src-toggle');
            toggle.type = 'button';
            toggle.setAttribute('aria-expanded', 'false');
            toggle.appendChild(this.doc.createTextNode('View sources'));
            toggle.appendChild(this.icon('chevron'));
            actions.appendChild(toggle);
            tail.appendChild(actions);

            const sources = this.node('div', 'story-sources');
            sources.hidden = true;
            sources.appendChild(this.buildSourceList(story.news.sources));
            tail.appendChild(sources);
            toggle.addEventListener('click', () => {
              const open = sources.hidden;
              sources.hidden = !open;
              toggle.classList.toggle('open', open);
              toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
              toggle.firstChild.textContent = open ? 'Hide sources' : 'View sources';
            });
          }

          if (story.tracks.length) {
            const tracks = this.node('div', 'story-tracks');
            story.tracks.forEach(track => tracks.appendChild(this.buildTrackRow(track)));
            tail.appendChild(tracks);
          }
          card.appendChild(tail);
        }
        playlist.appendChild(card);
      });
      this.highlight();
      this.updateUpNext();
      if (this.index < 0) this.setNowPlaying(null);
      this.setPlayingUI(!this.audio.paused && this.index >= 0);
    }

    buildTrackRow(track) {
      const item = track.item;
      const row = this.node('button', 'track-row');
      row.type = 'button';
      row.dataset.i = String(track.index);
      row.dataset.type = item.type;
      row.setAttribute('aria-label', 'Play ' + item.title);
      const icon = this.node('span', 'track-icon');
      icon.appendChild(this.icon(item.type));
      const info = this.node('span', 'track-info');
      info.appendChild(this.node('span', 'track-kicker', item.type === 'intro' ? 'SPOKEN INTRO' : 'UP NEXT · MUSIC'));
      info.appendChild(this.node('span', 'track-title', item.title));
      if (item.subtitle) info.appendChild(this.node('span', 'track-sub', item.subtitle));
      const duration = this.node('span', 'track-dur', fmt(item.duration));
      row.appendChild(icon);
      row.appendChild(info);
      row.appendChild(duration);
      row.addEventListener('click', () => this.play(track.index));
      return row;
    }

    buildSourceList(sources) {
      const list = this.node('ul');
      sources.forEach(source => {
        const item = this.node('li');
        const link = this.node('a');
        link.target = '_blank';
        link.rel = 'noopener noreferrer';
        if (source.url) link.href = source.url;
        link.appendChild(this.node('span', 'newspaper', (source.newspaper || 'Source') + (source.title ? ' — ' : '')));
        link.appendChild(this.node('span', 'src-title', source.title || source.url || ''));
        link.appendChild(this.icon('external'));
        item.appendChild(link);
        list.appendChild(item);
      });
      return list;
    }

    highlight() {
      this.el.playlist.querySelectorAll('.track-row').forEach(row => {
        const active = Number(row.dataset.i) === this.index;
        row.classList.toggle('active', active);
        if (active) row.setAttribute('aria-current', 'true');
        else row.removeAttribute('aria-current');
      });
      this.el.playlist.querySelectorAll('.story-card').forEach(card => {
        const first = Number(card.dataset.first);
        const last = Number(card.dataset.last);
        const active = card.dataset.first !== undefined && this.index >= first && this.index <= last;
        card.classList.toggle('active', active);
      });
    }

    updateMetadata() {
      const playlist = this.playlist;
      const stories = this.items.filter(item => item.type === 'news').length;
      const duration = this.items.reduce((sum, item) => sum + (Number(item.duration) || 0), 0);
      const label = this.genreLabels[this.activeGenre] || 'Mix';
      const slot = playlist && playlist.slot ? playlist.slot : '';
      this.el['story-count'].textContent = this.items.length ? String(stories).padStart(2, '0') : '—';
      this.el['briefing-duration'].textContent = this.items.length ? runtime(duration) : '—';
      this.el['active-genre-stat'].textContent = label;
      this.el['session-channel'].textContent = label;
      this.el['session-genre'].textContent = label === 'Mix' ? 'The daily mix' : label + ' frequency';
      this.el['session-count'].textContent = this.items.length ? String(this.items.length) : '—';
      this.el['session-edition'].textContent = slot ? slot.charAt(0).toUpperCase() + slot.slice(1) : '—';
      this.el['briefing-date'].textContent = playlist ? editionDate(playlist.date, playlist.created) : 'Your next audio edition';
      this.el['briefing-slot'].textContent = slot ? slot.toUpperCase() + ' EDITION · NEWS + MUSIC' : 'NEWS · MUSIC · MADE FOR YOU';
      this.el['briefing-kicker'].textContent = slot ? 'THE ' + slot.toUpperCase() + ' EDITION · ATHENS' : 'YOUR DAILY EDITION';
      if (this.podcastMode) {
        this.el['briefing-slot'].textContent = 'NINA SIMONE · SPEECH + MUSIC';
        this.el['briefing-kicker'].textContent = 'PODCAST EDITION · SPECIAL';
        this.el['session-edition'].textContent = 'Podcast';
        this.el['session-genre'].textContent = label === 'Full Guide' ? 'The full guide' : label;
      }
    }

    setNowPlaying(item) {
      if (!item) {
        this.el['now-type'].textContent = this.podcastMode ? 'N\u014cEMA PODCAST' : 'YOUR DAILY BRIEFING';
        this.el['now-playing'].textContent = this.items.length ? 'Ready when you are' : 'The airwaves are quiet';
        this.el['now-subtitle'].textContent = this.items.length ? 'Select a story or press play' : 'Generate an edition to begin';
        this.el.total.textContent = '0:00';
        return;
      }
      this.el['now-type'].textContent = TYPE_LABELS[item.type] || 'NOW PLAYING';
      this.el['now-playing'].textContent = item.title;
      this.el['now-subtitle'].textContent = item.subtitle || (item.type === 'news' ? 'Daily audio briefing' : 'N\u014dma personal radio');
      this.el.total.textContent = fmt(item.duration);
    }

    updateUpNext() {
      let nextItem = null;
      if (this.items.length) nextItem = this.items[this.index + 1] || (this.index < 0 ? this.items[0] : null);
      const iconHolder = this.el['up-next-icon'];
      iconHolder.replaceChildren();
      iconHolder.appendChild(this.icon(nextItem ? nextItem.type : 'news'));
      this.el['up-next-title'].textContent = nextItem ? nextItem.title : (this.items.length ? 'End of this edition' : 'Your briefing');
      this.el['up-next-subtitle'].textContent = nextItem
        ? (nextItem.subtitle || TYPE_LABELS[nextItem.type] || 'Up next')
        : (this.items.length ? 'You’re all caught up' : 'Press play to begin');
    }

    play(index) {
      if (index < 0 || index >= this.items.length) return;
      this.index = index;
      const item = this.items[index];
      this.pendingResumeTime = null;
      this.audio.src = item.url;
      this.el.seek.value = 0;
      this.el.time.textContent = '0:00';
      this.setNowPlaying(item);
      this.highlight();
      this.updateUpNext();
      const attempt = this.audio.play();
      if (attempt && typeof attempt.catch === 'function') attempt.catch(() => this.setPlayingUI(false));
      this.save();
    }

    togglePlay() {
      if (!this.items.length) return;
      if (this.index < 0) this.play(0);
      else if (this.audio.paused) {
        const attempt = this.audio.play();
        if (attempt && typeof attempt.catch === 'function') attempt.catch(() => this.setPlayingUI(false));
      } else this.audio.pause();
    }

    next() {
      if (this.index + 1 < this.items.length) this.play(this.index + 1);
    }

    previous() {
      if (this.audio.currentTime > 5) {
        this.audio.currentTime = 0;
        return;
      }
      this.play(this.index - 1);
    }

    seek() {
      const duration = this.currentDuration();
      if (duration) this.audio.currentTime = (Number(this.el.seek.value) / 100) * duration;
    }

    currentDuration() {
      if (Number.isFinite(this.audio.duration) && this.audio.duration > 0) return this.audio.duration;
      const item = this.items[this.index];
      return item ? Number(item.duration) || 0 : 0;
    }

    onEnded() {
      if (this.index + 1 < this.items.length) this.play(this.index + 1);
      else {
        this.setPlayingUI(false);
        this.showNotice('Edition complete. You’re all caught up.');
      }
    }

    onAudioError() {
      if (this.index >= 0 && this.index + 1 < this.items.length) this.play(this.index + 1);
      else if (this.index >= 0) {
        this.setPlayingUI(false);
        this.showNotice('This audio clip could not be played.', 'error');
      }
    }

    onTimeUpdate() {
      const duration = this.currentDuration();
      if (duration) {
        this.el.seek.value = Math.min(100, (this.audio.currentTime / duration) * 100);
        this.el.time.textContent = fmt(this.audio.currentTime);
        this.el.total.textContent = fmt(duration);
      }
      this.save();
    }

    onLoadedMetadata() {
      if (this.pendingResumeTime !== null) {
        const duration = this.currentDuration();
        const target = duration ? Math.min(this.pendingResumeTime, Math.max(0, duration - 0.1)) : this.pendingResumeTime;
        try { this.audio.currentTime = target; } catch (error) { /* Metadata can still be settling in some browsers. */ }
        this.pendingResumeTime = null;
      }
      this.el.total.textContent = fmt(this.currentDuration());
      this.onTimeUpdate();
    }

    setPlayingUI(playing) {
      this.el.player.classList.toggle('is-playing', playing);
      if (this.doc.body) this.doc.body.classList.toggle('is-playing', playing);
      this.el['play-icon'].setAttribute('href', playing ? '#icon-pause' : '#icon-play');
      this.el['hero-play-icon'].setAttribute('href', playing ? '#icon-pause' : '#icon-play');
      this.el.play.setAttribute('aria-label', playing ? 'Pause' : 'Play');
      this.el['hero-play-label'].textContent = playing ? 'Pause listening' : (this.index >= 0 ? 'Resume listening' : 'Start listening');
      this.el.play.disabled = !this.items.length;
      this.el['hero-play'].disabled = !this.items.length;
      this.el.prev.disabled = !this.items.length;
      this.el.next.disabled = !this.items.length;
    }

    readSaved() {
      try { return JSON.parse(this.storage.getItem('briefing') || 'null'); }
      catch (error) { return null; }
    }

    save() {
      if (!this.briefingId) return;
      try {
        this.storage.setItem('briefing', JSON.stringify({
          id: this.briefingId,
          index: this.index,
          time: Number(this.audio.currentTime) || 0,
          genre: this.activeGenre
        }));
      } catch (error) { /* Private browsing and full storage should not stop playback. */ }
    }

    resume() {
      const saved = this.readSaved();
      if (!saved || saved.id !== this.briefingId || saved.index < 0 || saved.index >= this.items.length) return;
      this.index = saved.index;
      const item = this.items[this.index];
      this.pendingResumeTime = Number(saved.time) || 0;
      this.audio.src = item.url;
      try { this.audio.currentTime = this.pendingResumeTime; } catch (error) { /* Applied again on loadedmetadata. */ }
      this.setNowPlaying(item);
      this.highlight();
      this.updateUpNext();
      this.setPlayingUI(false);
    }

    setGenerating(generating, slot) {
      const button = this.el['empty-action'];
      button.disabled = generating;
      button.setAttribute('aria-label', generating ? 'Generating ' + (slot || '') + ' briefing' : 'Create an edition');
    }

    async generate() {
      if (this.polling || this.el['empty-action'].disabled) return;
      const slot = generationSlot(this.el['briefing-select'].value, new Date().getHours());
      this.setGenerating(true, slot);
      this.showNotice('Composing your ' + slot + ' edition. News, voices and music are being prepared…', 'busy');
      try {
        const response = await this.fetch('/api/generate?slot=' + encodeURIComponent(slot), {
          method: 'POST',
          headers: { 'X-Requested-With': 'fetch' }
        });
        if (!response.ok && response.status !== 409) {
          let message = 'Generation request was rejected.';
          try {
            const body = await response.json();
            if (body && body.error) message = body.error;
          } catch (error) { /* Keep the friendly fallback. */ }
          throw new Error(message);
        }
        await this.pollStatus();
      } catch (error) {
        this.setGenerating(false);
        this.showNotice(error.message || 'Generation could not be started.', 'error');
      }
    }

    async pollStatus() {
      if (this.polling) return;
      this.polling = true;
      try {
        for (let attempt = 0; attempt < 450; attempt++) {
          await this.sleep(2000);
          try {
            const status = await this.request('/api/status');
            if (status.state === 'running') continue;
            this.setGenerating(false);
            if (status.state === 'error') {
              this.showNotice('Generation failed: ' + (status.last_error || 'Unknown error'), 'error');
            } else {
              await this.loadBriefings();
              await this.loadPlaylist('/api/latest', '');
              this.showNotice('Your new edition is ready. Press play and enjoy.');
            }
            return;
          } catch (error) {
            // A transient network error should not abandon a long generation.
          }
        }
        this.showNotice('Generation is taking longer than expected. Please refresh to check its status.', 'error');
      } finally {
        this.polling = false;
        this.setGenerating(false);
      }
    }

    async checkInitialStatus() {
      try {
        const status = await this.request('/api/status');
        if (status && status.state === 'running') {
          this.setGenerating(true, status.current_slot);
          this.showNotice('A new edition is being composed. You can keep listening while it finishes.', 'busy');
          this.pollStatus();
        }
      } catch (error) { /* Status polling is optional during initialization. */ }
    }

    showNotice(message, type) {
      this.el.notice.hidden = false;
      this.el.notice.className = 'notice' + (type ? ' is-' + type : '');
      this.el['notice-text'].textContent = message;
    }

    clearNotice() {
      this.el.notice.hidden = true;
      this.el.notice.className = 'notice';
      this.el['notice-text'].textContent = '';
    }

    onKeydown(event) {
      const target = event.target;
      const tag = target && target.tagName ? target.tagName.toUpperCase() : '';
      if (tag === 'INPUT' || tag === 'SELECT' || tag === 'TEXTAREA' || (target && target.isContentEditable)) return;
      if (event.ctrlKey || event.metaKey || event.altKey) return;
      switch (event.key) {
        case ' ':
          if (tag === 'BUTTON' || tag === 'A') return;
          event.preventDefault();
          this.togglePlay();
          break;
        case 'ArrowLeft':
          this.audio.currentTime = Math.max(0, (Number(this.audio.currentTime) || 0) - 5);
          break;
        case 'ArrowRight': {
          const duration = this.currentDuration();
          const nextTime = (Number(this.audio.currentTime) || 0) + 5;
          this.audio.currentTime = duration ? Math.min(duration, nextTime) : nextTime;
          break;
        }
        case 'n': case 'N': this.next(); break;
        case 'p': case 'P': this.previous(); break;
      }
    }
  }

  return { BriefingPlayer, fmt, runtime, sortBriefings, buildVariants, groupIntoStories, generationSlot, editionDate };
});
