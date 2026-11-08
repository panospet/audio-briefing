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

  const icons = { news: '📰', intro: '🎙️', music: '🎵' };

  function fmt(s) {
    if (!isFinite(s) || s < 0) s = 0;
    const m = Math.floor(s / 60);
    const sec = Math.floor(s % 60);
    return m + ':' + String(sec).padStart(2, '0');
  }

  function slotRank(slot) {
    return slot === 'evening' ? 2 : 1;
  }

  function syncSelect() {
    if (!briefingId) return;
    for (const opt of selectEl.options) {
      if (opt.value === briefingId) {
        selectEl.value = briefingId;
        break;
      }
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

  function loadPlaylist(url, id) {
    resetPlayerState();
    fetch(url).then(r => {
      if (!r.ok) throw new Error('not found');
      return r.json();
    }).then(p => {
      items = p.items || [];
      briefingId = id || (p.date && p.slot ? p.date + '/' + p.slot : (p.created || ''));
      syncSelect();
      render();
      if (items.length > 0) {
        nowPlayingEl.textContent = 'Ready — press play.';
      }
      resume();
    }).catch(() => {
      items = [];
      render();
      nowPlayingEl.textContent = 'No briefing yet — click "Generate now".';
    });
  }

  function render() {
    playlistEl.replaceChildren();
    items.forEach((it, i) => {
      const li = document.createElement('li');
      li.className = 'item';
      li.dataset.i = i;
      const icon = document.createElement('span');
      icon.className = 'icon';
      icon.textContent = icons[it.type] || '•';
      const meta = document.createElement('div');
      meta.className = 'meta';
      const title = document.createElement('div');
      title.className = 'title';
      title.textContent = it.title;
      meta.appendChild(title);
      if (it.subtitle) {
        const sub = document.createElement('div');
        sub.className = 'subtitle';
        sub.textContent = it.subtitle;
        meta.appendChild(sub);
      }
      const dur = document.createElement('span');
      dur.className = 'dur';
      dur.textContent = fmt(it.duration);
      li.appendChild(icon);
      li.appendChild(meta);
      li.appendChild(dur);
      li.addEventListener('click', () => play(i));
      playlistEl.appendChild(li);
    });
  }

  function highlight() {
    const lis = playlistEl.querySelectorAll('.item');
    lis.forEach((li, i) => li.classList.toggle('active', i === index));
  }

  function play(i) {
    if (i < 0 || i >= items.length) return;
    index = i;
    const it = items[index];
    audio.src = it.url;
    nowPlayingEl.textContent = (icons[it.type] || '') + ' ' + it.title;
    highlight();
    audio.play().catch(() => {
      playBtn.textContent = '▶';
    });
    save();
  }

  function next() { play(index + 1); }
  function prev() {
    if (audio.currentTime > 5) { audio.currentTime = 0; return; }
    play(index - 1);
  }

  function save() {
    if (!briefingId || index < 0) return;
    try {
      localStorage.setItem('briefing', JSON.stringify({
        id: briefingId, index, time: audio.currentTime
      }));
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
    if (index >= 0 && index + 1 < items.length) {
      play(index + 1);
    } else {
      playBtn.textContent = '▶';
    }
  });
  audio.addEventListener('timeupdate', () => {
    if (audio.duration) {
      seekEl.value = (audio.currentTime / audio.duration) * 100;
      timeEl.textContent = fmt(audio.currentTime);
      totalEl.textContent = fmt(audio.duration);
    }
    save();
  });
  audio.addEventListener('loadedmetadata', () => {
    totalEl.textContent = fmt(audio.duration);
  });

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
    if (selectEl.value && selectEl.value.includes('/evening')) {
      slot = 'evening';
    } else if (!selectEl.value && new Date().getHours() >= 18) {
      slot = 'evening';
    }
    try {
      const r = await fetch('/api/generate?slot=' + encodeURIComponent(slot), {
        method: 'POST',
        headers: { 'X-Requested-With': 'fetch' }
      });
      if (!r.ok && r.status !== 409) {
        throw new Error('request rejected');
      }
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
  loadBriefings().then(() => {
    loadPlaylist('/api/latest', '');
  });
  checkInitialStatus();
})();
