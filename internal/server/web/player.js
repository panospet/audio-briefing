(function () {
  const audio = document.getElementById('audio');
  const playlistEl = document.getElementById('playlist');
  const selectEl = document.getElementById('briefing-select');
  const nowPlayingEl = document.getElementById('now-playing');
  const playBtn = document.getElementById('play');
  const prevBtn = document.getElementById('prev');
  const nextBtn = document.getElementById('next');
  const seekEl = document.getElementById('seek');
  const volumeEl = document.getElementById('volume');
  const timeEl = document.getElementById('time');
  const totalEl = document.getElementById('total');
  const genBtn = document.getElementById('generate-btn');

  let items = [];
  let index = -1;
  let briefingId = '';
  let polling = false;

  let variants = {};      // genre id -> items array
  let genreOrder = [];    // ordered genre ids for the button bar
  let genreLabels = {};   // genre id -> display label
  let activeGenre = null;

  const icons = { news: '📰', intro: '🎙️', music: '🎵' };

  function fmt(s) {
    if (!isFinite(s) || s < 0) s = 0;
    const m = Math.floor(s / 60);
    const sec = Math.floor(s % 60);
    return m + ':' + String(sec).padStart(2, '0');
  }

  function slotRank(slot) { return slot === 'evening' ? 2 : 1; }

  function syncSelect() {
    if (!briefingId) return;
    for (const opt of selectEl.options) {
      if (opt.value === briefingId) { selectEl.value = briefingId; break; }
    }
  }

  function loadBriefings() {
    return fetch('/api/briefings').then(r => r.json()).then(list => {
      selectEl.replaceChildren();
      if (!list || !list.length) {
        const opt = document.createElement('option');
        opt.value = '';
        opt.textContent = '— no briefings yet —';
        selectEl.appendChild(opt);
        return;
      }
      list.sort((a, b) => {
        if (a.date !== b.date) return b.date.localeCompare(a.date);
        return slotRank(b.slot) - slotRank(a.slot);
      });
      for (const b of list) {
        const opt = document.createElement('option');
        opt.value = b.date + '/' + b.slot;
        opt.textContent = b.date + ' · ' + b.slot;
        selectEl.appendChild(opt);
      }
      syncSelect();
    }).catch(() => {});
  }

  function resetPlayerState() {
    audio.pause();
    audio.removeAttribute('src');
    index = -1;
    seekEl.value = 0;
    timeEl.textContent = '0:00';
    totalEl.textContent = '0:00';
    playBtn.textContent = '▶';
  }

  function buildVariants(p) {
    variants = {};
    genreOrder = [];
    genreLabels = {};
    const def = p.default || 'mix';
    variants[def] = p.items || [];
    genreLabels[def] = def === 'mix' ? 'Mix' : def;
    (p.genres || []).forEach(g => {
      genreLabels[g.id] = g.label || g.id;
      variants[g.id] = (g.id === def) ? (p.items || []) : ((p.variants && p.variants[g.id]) || []);
      if (genreOrder.indexOf(g.id) < 0) genreOrder.push(g.id);
    });
    if (genreOrder.indexOf(def) < 0) genreOrder.unshift(def);
  }

  function chooseGenre() {
    let savedGenre = null;
    try {
      const s = JSON.parse(localStorage.getItem('briefing') || 'null');
      if (s && s.id === briefingId) savedGenre = s.genre;
    } catch (e) {}
    if (savedGenre && variants[savedGenre]) return savedGenre;
    return genreOrder[0] || 'mix';
  }

  function loadPlaylist(url, id) {
    resetPlayerState();
    fetch(url).then(r => {
      if (!r.ok) throw new Error('not found');
      return r.json();
    }).then(p => {
      briefingId = id || (p.date && p.slot ? p.date + '/' + p.slot : (p.created || ''));
      buildVariants(p);
      activeGenre = chooseGenre();
      items = variants[activeGenre] || [];
      syncSelect();
      renderGenreBar();
      render();
      if (items.length > 0) nowPlayingEl.textContent = 'Ready — press play.';
      resume();
    }).catch(() => {
      variants = {}; genreOrder = []; genreLabels = {}; activeGenre = null;
      items = [];
      renderGenreBar();
      render();
      nowPlayingEl.textContent = 'No briefing yet — click "Generate now".';
    });
  }

  function renderGenreBar() {
    const bar = document.getElementById('genre-bar');
    if (!bar) return;
    bar.replaceChildren();
    if (genreOrder.length <= 1) { bar.style.display = 'none'; return; }
    bar.style.display = '';
    genreOrder.forEach(id => {
      const b = document.createElement('button');
      b.className = 'genre-btn' + (id === activeGenre ? ' active' : '');
      b.textContent = genreLabels[id] || id;
      b.addEventListener('click', () => selectGenre(id));
      bar.appendChild(b);
    });
  }

  function selectGenre(id) {
    if (!(id in variants) || id === activeGenre) return;
    activeGenre = id;
    const arr = variants[id] || [];
    audio.pause();
    audio.removeAttribute('src');
    index = -1;
    seekEl.value = 0;
    timeEl.textContent = '0:00';
    totalEl.textContent = '0:00';
    playBtn.textContent = '▶';
    items = arr;
    render();
    renderGenreBar();
    nowPlayingEl.classList.add('muted');
    nowPlayingEl.textContent = arr.length ? 'Ready — press play.' : 'No tracks in this genre yet.';
    save();
  }

  /* ---------- Grouping ---------- */
  function groupIntoStories(its) {
    const stories = [];
    let cur = null;
    its.forEach((it, i) => {
      if (it.type === 'news') {
        if (cur) stories.push(cur);
        cur = { news: it, newsIndex: i, tracks: [] };
      } else {
        if (!cur) cur = { news: null, newsIndex: -1, tracks: [] };
        cur.tracks.push({ item: it, index: i });
      }
    });
    if (cur) stories.push(cur);
    return stories;
  }

  /* ---------- Source list (all sources, no cap) ---------- */
  function buildSourceList(srcs) {
    const wrap = document.createElement('div');
    const ul = document.createElement('ul');
    wrap.appendChild(ul);

    srcs.forEach((s) => {
      const li = document.createElement('li');
      const a = document.createElement('a');
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
      if (s.url) a.href = s.url;
      const np = document.createElement('span');
      np.className = 'newspaper';
      np.textContent = (s.newspaper || '') + (s.newspaper && s.title ? ' — ' : '');
      const tt = document.createElement('span');
      tt.className = 'src-title';
      tt.textContent = s.title || s.url || '';
      a.appendChild(np);
      a.appendChild(tt);
      li.appendChild(a);
      ul.appendChild(li);
    });
    return wrap;
  }

  /* ---------- Render ---------- */
  function render() {
    playlistEl.replaceChildren();
    if (items.length === 0) return;

    const stories = groupIntoStories(items);

    stories.forEach((story, si) => {
      const card = document.createElement('li');
      card.className = 'story-card';
      card.dataset.story = si;
      if (story.newsIndex >= 0) {
        card.dataset.first = story.newsIndex;
        card.dataset.last = story.tracks.length
          ? story.tracks[story.tracks.length - 1].index
          : story.newsIndex;
      }

      // ---- Head ----
      const head = document.createElement('div');
      head.className = 'story-head';

      const icon = document.createElement('span');
      icon.className = 'story-icon';
      icon.textContent = story.news ? (icons[story.news.type] || '•') : '•';

      const meta = document.createElement('div');
      meta.className = 'story-meta';
      const title = document.createElement('div');
      title.className = 'story-title';
      title.textContent = story.news ? story.news.title : '—';
      meta.appendChild(title);

      const sub = document.createElement('div');
      sub.className = 'story-sub';
      if (story.news && story.news.subtitle) {
        const badge = document.createElement('span');
        badge.className = 'category-badge';
        badge.textContent = story.news.subtitle;
        sub.appendChild(badge);
      }
      if (story.news) {
        const dur = document.createElement('span');
        dur.textContent = fmt(story.news.duration);
        sub.appendChild(dur);
      }
      meta.appendChild(sub);

      const durEl = document.createElement('span');
      durEl.className = 'story-dur';
      durEl.textContent = story.news ? fmt(story.news.duration) : '';

      const toggle = document.createElement('button');
      toggle.className = 'src-toggle';
      toggle.textContent = '▸';
      toggle.title = 'Πηγές';
      toggle.setAttribute('aria-label', 'Πηγές');

      head.appendChild(icon);
      head.appendChild(meta);
      head.appendChild(durEl);
      if (story.news && story.news.sources && story.news.sources.length) {
        head.appendChild(toggle);
      }
      card.appendChild(head);

      head.addEventListener('click', () => {
        if (story.newsIndex >= 0) play(story.newsIndex);
      });

      // ---- Sources (expandable) ----
      if (story.news && story.news.sources && story.news.sources.length) {
        const srcWrap = document.createElement('div');
        srcWrap.className = 'story-sources';
        const inner = buildSourceList(story.news.sources);
        srcWrap.appendChild(inner);
        card.appendChild(srcWrap);

        toggle.addEventListener('click', (e) => {
          e.stopPropagation();
          const open = card.classList.toggle('sources-open');
          toggle.classList.toggle('open', open);
          toggle.textContent = open ? '▾' : '▸';
        });
      }

      // ---- Tracks ----
      if (story.tracks.length) {
        const tracks = document.createElement('div');
        tracks.className = 'story-tracks';
        story.tracks.forEach(t => {
          const row = document.createElement('div');
          row.className = 'track-row';
          row.dataset.i = t.index;
          const ti = document.createElement('span');
          ti.className = 'track-icon';
          ti.textContent = icons[t.item.type] || '•';
          const info = document.createElement('div');
          info.className = 'track-info';
          const tt = document.createElement('div');
          tt.className = 'track-title';
          tt.textContent = t.item.title;
          info.appendChild(tt);
          if (t.item.subtitle) {
            const ts = document.createElement('div');
            ts.className = 'track-sub';
            ts.textContent = t.item.subtitle;
            info.appendChild(ts);
          }
          const td = document.createElement('span');
          td.className = 'track-dur';
          td.textContent = fmt(t.item.duration);
          row.appendChild(ti);
          row.appendChild(info);
          row.appendChild(td);
          row.addEventListener('click', (e) => {
            e.stopPropagation();
            play(t.index);
          });
          tracks.appendChild(row);
        });
        card.appendChild(tracks);
      }

      playlistEl.appendChild(card);
    });

    highlight();
  }

  function highlight() {
    playlistEl.querySelectorAll('.track-row').forEach(row => {
      const i = parseInt(row.dataset.i, 10);
      row.classList.toggle('active', i === index);
    });
    playlistEl.querySelectorAll('.story-card').forEach(card => {
      const first = parseInt(card.dataset.first, 10);
      const last = parseInt(card.dataset.last, 10);
      if (!isNaN(first) && !isNaN(last)) {
        card.classList.toggle('active', index >= first && index <= last);
      }
    });
  }

  function play(i) {
    if (i < 0 || i >= items.length) return;
    index = i;
    const it = items[index];
    audio.src = it.url;
    nowPlayingEl.textContent = (icons[it.type] || '') + ' ' + it.title;
    nowPlayingEl.classList.remove('muted');
    highlight();
    audio.play().catch(() => { playBtn.textContent = '▶'; });
    save();
  }

  function next() { play(index + 1); }
  function prev() {
    if (audio.currentTime > 5) { audio.currentTime = 0; return; }
    play(index - 1);
  }

  function save() {
    if (!briefingId) return;
    try {
      localStorage.setItem('briefing', JSON.stringify({ id: briefingId, index, time: audio.currentTime, genre: activeGenre }));
    } catch (e) {}
  }

  function resume() {
    try {
      const saved = JSON.parse(localStorage.getItem('briefing') || 'null');
      if (saved && saved.id === briefingId && saved.index >= 0 && saved.index < items.length) {
        index = saved.index;
        audio.src = items[index].url;
        audio.currentTime = saved.time || 0;
        nowPlayingEl.textContent = (icons[items[index].type] || '') + ' ' + items[index].title;
        nowPlayingEl.classList.remove('muted');
        highlight();
      }
    } catch (e) {}
  }

  // audio events
  audio.addEventListener('play', () => { playBtn.textContent = '⏸'; });
  audio.addEventListener('pause', () => { playBtn.textContent = '▶'; });
  audio.addEventListener('ended', () => {
    if (index + 1 < items.length) play(index + 1);
    else { playBtn.textContent = '▶'; }
  });
  audio.addEventListener('error', () => {
    if (index >= 0 && index + 1 < items.length) play(index + 1);
    else { playBtn.textContent = '▶'; }
  });
  audio.addEventListener('timeupdate', () => {
    if (audio.duration) {
      seekEl.value = (audio.currentTime / audio.duration) * 100;
      timeEl.textContent = fmt(audio.currentTime);
      totalEl.textContent = fmt(audio.duration);
    }
    save();
  });
  audio.addEventListener('loadedmetadata', () => { totalEl.textContent = fmt(audio.duration); });

  playBtn.addEventListener('click', () => {
    if (items.length === 0) return;
    if (index < 0) play(0);
    else if (audio.paused) { audio.play().catch(() => {}); }
    else { audio.pause(); }
  });
  prevBtn.addEventListener('click', prev);
  nextBtn.addEventListener('click', next);

  seekEl.addEventListener('input', () => {
    if (audio.duration) audio.currentTime = (seekEl.value / 100) * audio.duration;
  });
  volumeEl.addEventListener('input', () => { audio.volume = volumeEl.value; });

  selectEl.addEventListener('change', () => {
    if (!selectEl.value) return;
    const [date, slot] = selectEl.value.split('/');
    loadPlaylist('/api/briefing?date=' + encodeURIComponent(date) + '&slot=' + encodeURIComponent(slot), date + '/' + slot);
  });

  genBtn.addEventListener('click', async () => {
    genBtn.disabled = true;
    genBtn.textContent = 'Generating…';
    let slot = 'morning';
    if (selectEl.value && selectEl.value.includes('/evening')) slot = 'evening';
    else if (!selectEl.value && new Date().getHours() >= 18) slot = 'evening';
    try {
      const r = await fetch('/api/generate?slot=' + encodeURIComponent(slot), {
        method: 'POST',
        headers: { 'X-Requested-With': 'fetch' }
      });
      if (!r.ok && r.status !== 409) throw new Error('request rejected');
      pollStatus();
    } catch (e) {
      genBtn.disabled = false;
      genBtn.textContent = '⟳ Generate now';
    }
  });

  async function pollStatus() {
    if (polling) return;
    polling = true;
    try {
      for (let i = 0; i < 450; i++) {
        await sleep(2000);
        try {
          const r = await fetch('/api/status');
          const s = await r.json();
          if (s.state !== 'running') {
            genBtn.disabled = false;
            genBtn.textContent = '⟳ Generate now';
            if (s.state === 'error') {
              nowPlayingEl.textContent = 'Generation failed: ' + (s.last_error || '');
              nowPlayingEl.classList.remove('muted');
            } else {
              await loadBriefings();
              loadPlaylist('/api/latest', '');
            }
            return;
          }
        } catch (e) { /* keep polling */ }
      }
    } finally {
      polling = false;
      genBtn.disabled = false;
      genBtn.textContent = '⟳ Generate now';
    }
  }

  function checkInitialStatus() {
    fetch('/api/status').then(r => r.json()).then(s => {
      if (s && s.state === 'running') {
        genBtn.disabled = true;
        genBtn.textContent = 'Generating…';
        pollStatus();
      }
    }).catch(() => {});
  }

  function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

  document.addEventListener('keydown', (e) => {
    if (e.target.tagName === 'INPUT' || e.target.tagName === 'SELECT') return;
    switch (e.key) {
      case ' ': e.preventDefault(); playBtn.click(); break;
      case 'ArrowLeft': audio.currentTime = Math.max(0, audio.currentTime - 5); break;
      case 'ArrowRight': audio.currentTime += 5; break;
      case 'n': case 'N': next(); break;
      case 'p': case 'P': prev(); break;
    }
  });

  // init
  loadBriefings().then(() => { loadPlaylist('/api/latest', ''); });
  checkInitialStatus();
})();
