(function() {
  'use strict';

  const SPARKLINE_LEN = 60;
  const CHART_LEN = 360; // largest tier capacity (medium = 1h × 10s buckets)
  let devices = {};
  let currentRange = '5min';
  // Header toggles. Both off by default — keeps the page compact and the
  // user explicitly opens what they want to see.
  let showProcs = false;
  let showCores = false;
  // Last received processes payload, so toggling the button can re-render
  // immediately without waiting for the next SSE event.
  let lastProcesses = null;

  // Per-module history: { moduleName: { metricKey: [{t, v}] } }
  const history = {};

  // Per-core history. Shape:
  //   coreUtilHistory[coreId] = [{t, v}, ...]
  //   coreFreqHistory[coreId] = [{t, v}, ...]
  //   coreTypes[coreId] = "performance" | "efficient" | "low_power"
  //   coreOrder = sorted list of core ids ever seen
  // Per-type average history (computed from per-core utilization):
  //   typeAvgHistory["performance"] = [{t, v}, ...]
  const coreUtilHistory = {};
  const coreFreqHistory = {};
  const coreTypes = {};
  let coreOrder = [];
  const typeAvgHistory = {
    performance: [],
    efficient: [],
    low_power: []
  };
  const TYPE_COLORS = {
    performance: '#58a6ff',
    efficient: '#3fb950',
    low_power: '#d29922'
  };
  const TYPE_LABELS = {
    performance: 'P-cores',
    efficient: 'E-cores',
    low_power: 'LP-cores'
  };

  // Metric definitions per module
  const cpuMetricDefs = [
    { key: 'utilization', label: 'Utilization', unit: '%',    precision: 1, color: '#58a6ff', max: 100,
      subtitleFn: function() {
        var arr = (history.cpu && history.cpu.cores_used) || [];
        if (arr.length === 0) return '';
        var n = (devices.cpu && devices.cpu.extra && devices.cpu.extra.threads) || '?';
        return arr[arr.length - 1].v.toFixed(1) + ' / ' + n + ' cores';
      } },
    { key: 'frequency',   label: 'Frequency',   unit: 'MHz',  precision: 0, color: '#39d2c0' },
    { key: 'power',       label: 'Power',       unit: 'W',    precision: 2, color: '#d29922' },
    { key: 'temperature', label: 'Temperature', unit: '\u00b0C', precision: 0, color: '#f85149' },
    { key: 'cores_used',  hidden: true },
  ];

  const npuMetricDefs = [
    { key: 'utilization',   label: 'Utilization',   unit: '%',    precision: 1, color: '#58a6ff', max: 100 },
    { key: 'frequency',     label: 'Frequency',     unit: 'MHz',  precision: 0, color: '#39d2c0' },
    { key: 'power',         label: 'Power',         unit: 'W',    precision: 2, color: '#d29922' },
    { key: 'temperature',   label: 'Temperature',   unit: '\u00b0C', precision: 0, color: '#f85149' },
    { key: 'ddr_bandwidth', label: 'DDR Bandwidth', unit: 'GB/s', precision: 2, color: '#bc8cff', transform: function(v) { return v / 1000; } },
    { key: 'tile_config',   label: 'Tile Config',   unit: '',     precision: 0, color: '#3fb950' },
    { key: 'memory_used_percent', label: 'Memory', unit: '%', precision: 0, color: '#39d2c0', max: 100,
      subtitleFn: function() {
        var used = (history.npu && history.npu.memory_used_gb) || [];
        var total = (history.npu && history.npu.memory_total_gb) || [];
        if (used.length === 0) return '';
        var u = used[used.length - 1].v;
        var t = total.length > 0 ? total[total.length - 1].v : 0;
        if (t > 0) return u.toFixed(1) + ' / ' + t.toFixed(1) + ' GB';
        return u.toFixed(1) + ' GB';
      } },
    { key: 'memory_used_gb',  srcKey: 'memory_used',  hidden: true, transform: function(v) { return v / (1024*1024*1024); } },
    { key: 'memory_total_gb', srcKey: 'memory_total', hidden: true, transform: function(v) { return v / (1024*1024*1024); } },
  ];

  const gpuMetricDefs = [
    { key: 'utilization',         label: 'Utilization',      unit: '%',   precision: 1, color: '#58a6ff', max: 100 },
    { key: 'frequency_actual',    label: 'Freq (actual)',    unit: 'MHz', precision: 0, color: '#39d2c0' },
    { key: 'frequency_requested', label: 'Freq (requested)', unit: 'MHz', precision: 0, color: '#58a6ff' },
    { key: 'power',               label: 'Power',            unit: 'W',   precision: 2, color: '#d29922' },
    { key: 'temperature',         label: 'Temperature',      unit: '\u00b0C', precision: 0, color: '#f85149' },
    { key: 'frequency_min',       label: 'Freq (min)',       unit: 'MHz', precision: 0, color: '#8b949e' },
    { key: 'frequency_max',       label: 'Freq (max)',       unit: 'MHz', precision: 0, color: '#8b949e' },
  ];

  const cpuChartSeries = [
    { key: 'utilization', color: '#58a6ff', min: 0 },
    { key: 'frequency',   color: '#39d2c0' },
    { key: 'power',       color: '#d29922' },
    { key: 'temperature', color: '#f85149' },
  ];

  const npuChartSeries = [
    { key: 'utilization', color: '#58a6ff', min: 0, max: 100 },
    { key: 'frequency',   color: '#39d2c0' },
    { key: 'power',       color: '#d29922' },
    { key: 'temperature', color: '#f85149' },
  ];

  const gpuChartSeries = [
    { key: 'utilization',      color: '#58a6ff', min: 0, max: 100 },
    { key: 'frequency_actual', color: '#39d2c0' },
    { key: 'power',            color: '#d29922' },
    { key: 'temperature',      color: '#f85149' },
  ];

  // --- Init ---
  async function init() {
    try {
      var histResp = await fetch('/api/metrics/history?range=' + currentRange);
      var devResp = await fetch('/api/devices');
      var histData = await histResp.json();
      devices = await devResp.json();

      if (histData && histData.length > 0) {
        for (var i = 0; i < histData.length; i++) {
          pushSample(histData[i]);
        }
      }

      // Hook up range selector.
      var rangeSel = document.getElementById('range-select');
      rangeSel.value = currentRange;
      rangeSel.addEventListener('change', function() {
        currentRange = rangeSel.value;
        reloadHistory();
      });

      // Header toggles for process lists and per-core details. Both
      // default to off; the user opens what they want.
      var procsBtn = document.getElementById('toggle-procs');
      procsBtn.addEventListener('click', function() {
        showProcs = !showProcs;
        procsBtn.classList.toggle('on', showProcs);
        // Re-render immediately using the cached last processes payload
        // instead of waiting for the next SSE tick.
        if (lastProcesses) updateProcesses(lastProcesses);
      });

      var coresBtn = document.getElementById('toggle-cores');
      coresBtn.addEventListener('click', function() {
        showCores = !showCores;
        coresBtn.classList.toggle('on', showCores);
        var body = document.getElementById('cpu-details');
        if (body) {
          body.style.display = showCores ? '' : 'none';
          if (showCores) drawPerCore();
        }
      });

      // Show sections based on available devices
      if (devices.cpu) {
        document.getElementById('cpu-section').style.display = '';
        buildCards('cpu', cpuMetricDefs, 'cpu-cards');
      }
      if (devices.npu) {
        document.getElementById('npu-section').style.display = '';
        buildCards('npu', npuMetricDefs, 'npu-cards');
      }
      if (devices.gpu) {
        document.getElementById('gpu-section').style.display = '';
        buildCards('gpu', gpuMetricDefs, 'gpu-cards');
      }

      buildInfoPanel();
      updateAllCards();
      drawAllCharts();
      setupCpuDetails();
      drawPerCore();

      document.getElementById('loading').style.display = 'none';
      document.getElementById('app').style.display = '';

      // Header badge
      var badges = [];
      if (devices.npu) {
        var gen = (devices.npu.extra && devices.npu.extra.generation) || '';
        badges.push(gen.toUpperCase() + ' \u00b7 ' + (devices.npu.pci_device || ''));
      }
      if (devices.gpu) {
        var drv = (devices.gpu.extra && devices.gpu.extra.driver) || '';
        badges.push(drv + ' \u00b7 ' + (devices.gpu.pci_device || ''));
      }
      document.getElementById('gen-badge').textContent = badges.join('  |  ');

      connectSSE();
    } catch (e) {
      document.getElementById('loading').innerHTML =
        '<div style="text-align:center"><div style="color:#f85149;font-size:18px;margin-bottom:8px">Connection failed</div>' +
        '<div style="color:#8b949e">' + e.message + '</div></div>';
    }
  }

  function pushSample(sample) {
    var t = new Date(sample.timestamp).getTime();
    pushModuleMetrics('cpu', cpuMetricDefs, sample.metrics && sample.metrics.cpu, t);
    pushModuleMetrics('npu', npuMetricDefs, sample.metrics && sample.metrics.npu, t);
    pushModuleMetrics('gpu', gpuMetricDefs, sample.metrics && sample.metrics.gpu, t);
    pushPerCoreFromSample(sample, t);
  }

  // Clear all history buffers and reload from the currently-selected tier.
  // Called when the user changes the range dropdown.
  async function reloadHistory() {
    // Wipe existing chart history (per-module + per-core + per-type avg).
    for (var k in history) delete history[k];
    for (var k2 in coreUtilHistory) delete coreUtilHistory[k2];
    for (var k3 in coreFreqHistory) delete coreFreqHistory[k3];
    typeAvgHistory.performance.length = 0;
    typeAvgHistory.efficient.length = 0;
    typeAvgHistory.low_power.length = 0;

    try {
      var resp = await fetch('/api/metrics/history?range=' + currentRange);
      var data = await resp.json();
      if (data && data.length > 0) {
        for (var i = 0; i < data.length; i++) pushSample(data[i]);
      }
      updateAllCards();
      drawAllCharts();
      drawPerCore();
    } catch (e) {
      console.error('range reload failed:', e);
    }
  }

  function pushPerCoreFromSample(sample, t) {
    var cpu = sample.metrics && sample.metrics.cpu;
    if (!cpu) return;
    // Aggregate per-type util in this single sample for the type-avg chart.
    var sums = { performance: 0, efficient: 0, low_power: 0 };
    var counts = { performance: 0, efficient: 0, low_power: 0 };
    var sawAny = false;
    for (var i = 0; i < cpu.length; i++) {
      var m = cpu[i];
      if (!m.labels || !m.labels.core || !m.labels.core_type) continue;
      var id = parseInt(m.labels.core, 10);
      if (isNaN(id)) continue;
      var ct = m.labels.core_type;
      coreTypes[id] = ct;
      if (m.name === 'core_utilization') {
        sawAny = true;
        if (!coreUtilHistory[id]) coreUtilHistory[id] = [];
        coreUtilHistory[id].push({ t: t, v: m.value });
        if (coreUtilHistory[id].length > CHART_LEN) {
          coreUtilHistory[id] = coreUtilHistory[id].slice(-CHART_LEN);
        }
        if (sums[ct] !== undefined) {
          sums[ct] += m.value;
          counts[ct]++;
        }
      } else if (m.name === 'core_frequency') {
        if (!coreFreqHistory[id]) coreFreqHistory[id] = [];
        coreFreqHistory[id].push({ t: t, v: m.value });
        if (coreFreqHistory[id].length > CHART_LEN) {
          coreFreqHistory[id] = coreFreqHistory[id].slice(-CHART_LEN);
        }
      }
    }
    if (!sawAny) return;

    // Refresh sorted core order if a new id appeared.
    var ids = Object.keys(coreTypes).map(Number);
    ids.sort(function(a, b) { return a - b; });
    coreOrder = ids;

    // Push per-type averages.
    var types = ['performance', 'efficient', 'low_power'];
    for (var ti = 0; ti < types.length; ti++) {
      var tt = types[ti];
      if (counts[tt] === 0) continue;
      typeAvgHistory[tt].push({ t: t, v: sums[tt] / counts[tt] });
      if (typeAvgHistory[tt].length > CHART_LEN) {
        typeAvgHistory[tt] = typeAvgHistory[tt].slice(-CHART_LEN);
      }
    }
  }

  function pushModuleMetrics(mod, defs, metrics, t) {
    if (!history[mod]) history[mod] = {};
    metrics = metrics || [];
    for (var i = 0; i < defs.length; i++) {
      var def = defs[i];
      if (!history[mod][def.key]) history[mod][def.key] = [];
      var srcName = def.srcKey || def.key;
      var m = null;
      for (var j = 0; j < metrics.length; j++) {
        if (metrics[j].name === srcName) { m = metrics[j]; break; }
      }
      if (m) {
        var v = m.value;
        if (def.transform) v = def.transform(v);
        history[mod][def.key].push({ t: t, v: v });
      }
      if (history[mod][def.key].length > CHART_LEN) {
        history[mod][def.key] = history[mod][def.key].slice(-CHART_LEN);
      }
    }
  }

  // --- SSE ---
  function connectSSE() {
    var es = new EventSource('/api/metrics/stream');
    es.onopen = function() { setStatus(true); };
    es.onmessage = function(evt) {
      try {
        var sample = JSON.parse(evt.data);
        pushSample(sample);
        updateAllCards();
        drawAllCharts();
        drawPerCore();
        updateProcesses(sample.processes);
        updateWarnings(sample.warnings);
      } catch (e) { /* ignore parse errors */ }
    };
    es.onerror = function() {
      setStatus(false);
      es.close();
      setTimeout(connectSSE, 3000);
    };
  }

  function setStatus(ok) {
    var dot = document.getElementById('status-dot');
    var text = document.getElementById('status-text');
    dot.className = ok ? 'status-dot' : 'status-dot disconnected';
    text.textContent = ok ? 'Connected' : 'Reconnecting...';
  }

  // --- Cards ---
  function buildCards(mod, defs, containerId) {
    var container = document.getElementById(containerId);
    container.innerHTML = '';
    for (var i = 0; i < defs.length; i++) {
      var def = defs[i];
      if (def.hidden) continue;
      var card = document.createElement('div');
      card.className = 'card';
      card.dataset.metric = def.key;
      card.id = 'card-' + mod + '-' + def.key;
      card.innerHTML =
        '<div class="card-label">' + def.label + '</div>' +
        '<div class="card-value"><span id="val-' + mod + '-' + def.key + '">\u2014</span>' +
        '<span class="card-unit">' + def.unit + '</span></div>' +
        '<div class="card-sub" id="sub-' + mod + '-' + def.key + '"></div>' +
        '<canvas id="spark-' + mod + '-' + def.key + '" height="40"></canvas>';
      container.appendChild(card);
    }
  }

  function updateAllCards() {
    updateModuleCards('cpu', cpuMetricDefs);
    updateModuleCards('npu', npuMetricDefs);
    updateModuleCards('gpu', gpuMetricDefs);
  }

  function updateModuleCards(mod, defs) {
    if (!history[mod]) return;
    for (var i = 0; i < defs.length; i++) {
      var def = defs[i];
      if (def.hidden) continue;
      var arr = history[mod][def.key] || [];
      var last = arr.length > 0 ? arr[arr.length - 1].v : null;
      var el = document.getElementById('val-' + mod + '-' + def.key);
      if (el) el.textContent = last !== null ? last.toFixed(def.precision) : '\u2014';
      var sub = document.getElementById('sub-' + mod + '-' + def.key);
      if (sub) sub.textContent = def.subtitleFn ? def.subtitleFn() : '';
      drawSparkline(mod, def);
    }
  }

  // --- Sparkline ---
  function drawSparkline(mod, def) {
    var canvas = document.getElementById('spark-' + mod + '-' + def.key);
    if (!canvas) return;
    var ctx = canvas.getContext('2d');
    var dpr = window.devicePixelRatio || 1;
    var w = canvas.clientWidth, h = canvas.clientHeight;
    canvas.width = w * dpr; canvas.height = h * dpr;
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, w, h);

    var arr = (history[mod] && history[mod][def.key]) || [];
    var data = arr.slice(-SPARKLINE_LEN);
    if (data.length < 2) return;

    var values = data.map(function(d) { return d.v; });
    var min = Math.min.apply(null, values);
    var max = Math.max.apply(null, values);
    if (def.max !== undefined) { min = 0; max = Math.max(max, def.max); }
    if (max === min) max = min + 1;

    var padY = 2, yRange = h - padY * 2;
    // Anchor to a fixed window so points enter from the right and scroll left
    // once the buffer is full, instead of compressing into the available width.
    var slots = SPARKLINE_LEN - 1;
    var startIdx = slots - (data.length - 1);

    ctx.beginPath();
    ctx.moveTo(((startIdx) / slots) * w, h);
    for (var i = 0; i < data.length; i++) {
      var x = ((startIdx + i) / slots) * w;
      var y = padY + yRange - ((data[i].v - min) / (max - min)) * yRange;
      ctx.lineTo(x, y);
    }
    ctx.lineTo(w, h);
    ctx.closePath();
    ctx.fillStyle = def.color + '18';
    ctx.fill();

    ctx.beginPath();
    for (var i = 0; i < data.length; i++) {
      var x = ((startIdx + i) / slots) * w;
      var y = padY + yRange - ((data[i].v - min) / (max - min)) * yRange;
      if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    }
    ctx.strokeStyle = def.color;
    ctx.lineWidth = 1.5;
    ctx.stroke();
  }

  // --- Main charts ---
  function drawAllCharts() {
    drawChart('cpu-chart', 'cpu', cpuChartSeries);
    drawChart('npu-chart', 'npu', npuChartSeries);
    drawChart('gpu-chart', 'gpu', gpuChartSeries);
  }

  function drawChart(canvasId, mod, series) {
    var canvas = document.getElementById(canvasId);
    if (!canvas) return;
    var ctx = canvas.getContext('2d');
    var dpr = window.devicePixelRatio || 1;
    var w = canvas.clientWidth, h = canvas.clientHeight;
    canvas.width = w * dpr; canvas.height = h * dpr;
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, w, h);

    // Gridlines
    ctx.strokeStyle = '#30363d';
    ctx.lineWidth = 0.5;
    for (var g = 0; g <= 4; g++) {
      var gy = (g / 4) * h;
      ctx.beginPath(); ctx.moveTo(0, gy); ctx.lineTo(w, gy); ctx.stroke();
    }

    if (!history[mod]) return;

    for (var si = 0; si < series.length; si++) {
      var s = series[si];
      var arr = history[mod][s.key] || [];
      if (arr.length < 2) continue;

      var values = arr.map(function(d) { return d.v; });
      var min = s.min !== undefined ? s.min : Math.min.apply(null, values);
      var max = s.max !== undefined ? s.max : Math.max.apply(null, values);
      if (max === min) max = min + 1;

      var padY = 4, yRange = h - padY * 2;
      var slots = CHART_LEN - 1;
      var startIdx = slots - (arr.length - 1);

      ctx.beginPath();
      ctx.moveTo((startIdx / slots) * w, h);
      for (var i = 0; i < arr.length; i++) {
        var x = ((startIdx + i) / slots) * w;
        var y = padY + yRange - ((arr[i].v - min) / (max - min)) * yRange;
        ctx.lineTo(x, y);
      }
      ctx.lineTo(w, h);
      ctx.closePath();
      ctx.fillStyle = s.color + '0d';
      ctx.fill();

      ctx.beginPath();
      for (var i = 0; i < arr.length; i++) {
        var x = ((startIdx + i) / slots) * w;
        var y = padY + yRange - ((arr[i].v - min) / (max - min)) * yRange;
        if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
      }
      ctx.strokeStyle = s.color;
      ctx.lineWidth = 1.5;
      ctx.stroke();
    }
  }

  // --- Info panel ---
  function buildInfoPanel() {
    var grid = document.getElementById('info-grid');
    grid.innerHTML = '';

    if (devices.cpu) {
      var sec = makeInfoSection('CPU');
      addInfoRow(sec, 'Model', devices.cpu.name || '\u2014');
      addInfoRow(sec, 'Vendor', (devices.cpu.extra && devices.cpu.extra.vendor) || '\u2014');
      var cores = (devices.cpu.extra && devices.cpu.extra.cores) || '?';
      var threads = (devices.cpu.extra && devices.cpu.extra.threads) || '?';
      addInfoRow(sec, 'Cores', cores + ' physical, ' + threads + ' threads');
      addInfoRow(sec, 'Arch', (devices.cpu.extra && devices.cpu.extra.arch) || '\u2014');
      grid.appendChild(sec);
    }

    if (devices.gpu) {
      var sec = makeInfoSection('GPU');
      addInfoRow(sec, 'Device', devices.gpu.name || '\u2014');
      addInfoRow(sec, 'PCI ID', devices.gpu.pci_device || '\u2014');
      addInfoRow(sec, 'Driver', (devices.gpu.extra && devices.gpu.extra.driver) || '\u2014');
      if (devices.gpu.extra && devices.gpu.extra.pci_slot) {
        addInfoRow(sec, 'PCI Slot', devices.gpu.extra.pci_slot);
      }
      grid.appendChild(sec);
    }

    if (devices.npu) {
      var sec = makeInfoSection('NPU');
      addInfoRow(sec, 'Device', devices.npu.name || '\u2014');
      addInfoRow(sec, 'PCI ID', devices.npu.pci_device || '\u2014');
      addInfoRow(sec, 'Driver', devices.npu.driver_version || '\u2014');
      addInfoRow(sec, 'Firmware', devices.npu.firmware_version || '\u2014');
      if (devices.npu.extra && devices.npu.extra.pci_slot) {
        addInfoRow(sec, 'PCI Slot', devices.npu.extra.pci_slot);
      }
      grid.appendChild(sec);
    }
  }

  function makeInfoSection(title) {
    var sec = document.createElement('div');
    sec.className = 'info-section';
    var h3 = document.createElement('h3');
    h3.textContent = title;
    sec.appendChild(h3);
    return sec;
  }

  function addInfoRow(parent, label, value) {
    var row = document.createElement('div');
    row.className = 'info-row';
    var l = document.createElement('span');
    l.className = 'label';
    l.textContent = label;
    var v = document.createElement('span');
    v.className = 'value';
    v.textContent = value;
    row.appendChild(l);
    row.appendChild(v);
    parent.appendChild(row);
  }

  // --- Warnings ---

  function updateWarnings(warnings) {
    var byMod = { cpu: [], gpu: [], npu: [] };
    if (warnings) {
      for (var i = 0; i < warnings.length; i++) {
        var w = warnings[i];
        if (byMod[w.module]) byMod[w.module].push(w);
      }
    }
    renderBadges('cpu-badges', byMod.cpu);
    renderBadges('gpu-badges', byMod.gpu);
    renderBadges('npu-badges', byMod.npu);
  }

  function renderBadges(containerId, list) {
    var el = document.getElementById(containerId);
    if (!el) return;
    if (!list || list.length === 0) {
      el.innerHTML = '';
      return;
    }
    // Critical first.
    list.sort(function(a, b) {
      if (a.severity === b.severity) return 0;
      return a.severity === 'critical' ? -1 : 1;
    });
    var html = '';
    for (var i = 0; i < list.length; i++) {
      var w = list[i];
      var since = new Date(w.since);
      var ageS = Math.max(0, Math.floor((Date.now() - since.getTime()) / 1000));
      var tip = w.message + ' (since ' + formatAge(ageS) + ')';
      html += '<span class="warn-badge ' + escapeHTML(w.severity) + '" title="' + escapeHTML(tip) + '">'
        + escapeHTML(w.kind) + '</span>';
    }
    el.innerHTML = html;
  }

  function formatAge(s) {
    if (s < 60) return s + 's';
    if (s < 3600) return Math.floor(s / 60) + 'm' + (s % 60) + 's';
    return Math.floor(s / 3600) + 'h' + Math.floor((s % 3600) / 60) + 'm';
  }

  // --- Process attribution ---

  function updateProcesses(procs) {
    if (!procs) return;
    lastProcesses = procs;
    // Always advance the sticky trackers so we have current data ready
    // the moment the user opens the panel. Rendering is gated by showProcs.
    feedCPUSticky(procs.cpu || []);
    feedGPUSticky(procs.gpu || []);
    renderProcPanels(procs);
  }

  function renderProcPanels(procs) {
    document.getElementById('cpu-procs').style.display = showProcs ? '' : 'none';
    document.getElementById('gpu-procs').style.display = showProcs ? '' : 'none';
    document.getElementById('npu-procs').style.display = (showProcs && procs && procs.npu && procs.npu.length > 0) ? '' : 'none';
    if (!showProcs) return;
    renderCPUProcs();
    renderGPUProcs();
    renderNPUProcs(procs && procs.npu);
  }

  // Sticky display lists keep the proc tables a fixed height. Once a process
  // appears in the top-N it stays visible for STICKY_TICKS more samples even
  // if it drops below the threshold (or out of the top-N entirely). This
  // stops the rows from popping in/out and the section from jumping around.
  var PROC_ROWS = 4;
  var STICKY_TICKS = 8; // ~8 seconds at 1Hz collection
  var stickyCPU = []; // [{pid, comm, cpu_percent, rss_bytes, ttl}]
  var stickyGPU = []; // [{pid, comm, total_busy, gtt_bytes, ttl}]

  function mergeSticky(sticky, fresh, valueKey, fields) {
    // Index sticky entries by pid for in-place updates.
    var idx = {};
    for (var i = 0; i < sticky.length; i++) idx[sticky[i].pid] = i;
    // Decay all existing entries one tick.
    for (var j = 0; j < sticky.length; j++) {
      sticky[j].ttl -= 1;
      // Don't reset value yet — we'll either update it from fresh or leave
      // it at its last value so the row still has something to show.
    }
    // Merge fresh entries: update existing or insert new.
    for (var k = 0; k < fresh.length; k++) {
      var f = fresh[k];
      if (idx[f.pid] !== undefined) {
        var existing = sticky[idx[f.pid]];
        for (var fk in fields) existing[fk] = f[fields[fk]];
        existing.comm = f.comm || existing.comm;
        existing.ttl = STICKY_TICKS;
      } else {
        var entry = { pid: f.pid, comm: f.comm, ttl: STICKY_TICKS };
        for (var fk2 in fields) entry[fk2] = f[fields[fk2]];
        sticky.push(entry);
      }
    }
    // Drop entries whose TTL has expired AND whose value is below the floor.
    var out = [];
    for (var m = 0; m < sticky.length; m++) {
      if (sticky[m].ttl > 0) out.push(sticky[m]);
    }
    // Sort by current value desc.
    out.sort(function(a, b) { return (b[valueKey] || 0) - (a[valueKey] || 0); });
    // Truncate to display limit. (Sticky entries beyond PROC_ROWS still age
    // out naturally; this just controls how many we show at once.)
    if (out.length > PROC_ROWS) out = out.slice(0, PROC_ROWS);
    return out;
  }

  function feedCPUSticky(list) {
    stickyCPU = mergeSticky(stickyCPU, list || [], 'cpu_percent', {
      cpu_percent: 'cpu_percent',
      rss_bytes:   'rss_bytes'
    });
  }

  function renderCPUProcs() {
    var table = document.getElementById('cpu-procs-table');
    var maxPct = 100;
    for (var k = 0; k < stickyCPU.length; k++) {
      if (stickyCPU[k].cpu_percent > maxPct) maxPct = stickyCPU[k].cpu_percent;
    }
    var html = '<thead><tr>'
      + '<th>Process</th>'
      + '<th class="col-pid">PID</th>'
      + '<th class="col-busy">CPU</th>'
      + '<th class="col-mem">RSS</th>'
      + '</tr></thead><tbody>';
    for (var i = 0; i < PROC_ROWS; i++) {
      var r = stickyCPU[i];
      if (r) {
        var pct = r.cpu_percent || 0;
        var barPct = (pct / maxPct) * 100;
        var fade = r.ttl < STICKY_TICKS ? ' style="opacity:' + (0.4 + 0.6 * r.ttl / STICKY_TICKS).toFixed(2) + '"' : '';
        html += '<tr' + fade + '>'
          + '<td>' + escapeHTML(r.comm || '?') + '</td>'
          + '<td class="col-pid">' + r.pid + '</td>'
          + '<td class="col-busy">'
          +   '<div class="bar"><div class="bar-fill" style="width:' + barPct.toFixed(1) + '%"></div></div>'
          +   '<div style="font-size:10px;color:#8b949e">' + pct.toFixed(0) + '%</div>'
          + '</td>'
          + '<td class="col-mem">' + formatBytes(r.rss_bytes || 0) + '</td>'
          + '</tr>';
      } else {
        html += '<tr class="proc-empty"><td>&nbsp;</td><td class="col-pid"></td>'
          + '<td class="col-busy"><div class="bar"></div></td>'
          + '<td class="col-mem"></td></tr>';
      }
    }
    html += '</tbody>';
    table.innerHTML = html;
  }

  function feedGPUSticky(list) {
    // Aggregate by PID (a process may hold several DRM clients).
    var byPid = {};
    for (var i = 0; i < (list || []).length; i++) {
      var p = list[i];
      var k = p.pid;
      if (!byPid[k]) {
        byPid[k] = { pid: p.pid, comm: p.comm, total_busy: 0, gtt_bytes: 0 };
      }
      byPid[k].total_busy += p.total_busy || 0;
      byPid[k].gtt_bytes += p.gtt_bytes || 0;
    }
    var fresh = Object.values(byPid);
    stickyGPU = mergeSticky(stickyGPU, fresh, 'total_busy', {
      total_busy: 'total_busy',
      gtt_bytes:  'gtt_bytes'
    });
  }

  function renderGPUProcs() {
    var table = document.getElementById('gpu-procs-table');
    var html = '<thead><tr>'
      + '<th>Process</th>'
      + '<th class="col-pid">PID</th>'
      + '<th class="col-busy">Busy</th>'
      + '<th class="col-mem">GTT</th>'
      + '</tr></thead><tbody>';
    for (var j = 0; j < PROC_ROWS; j++) {
      var r = stickyGPU[j];
      if (r) {
        var pct = Math.min(100, (r.total_busy || 0) * 100);
        var fade = r.ttl < STICKY_TICKS ? ' style="opacity:' + (0.4 + 0.6 * r.ttl / STICKY_TICKS).toFixed(2) + '"' : '';
        html += '<tr' + fade + '>'
          + '<td>' + escapeHTML(r.comm || '?') + '</td>'
          + '<td class="col-pid">' + r.pid + '</td>'
          + '<td class="col-busy">'
          +   '<div class="bar"><div class="bar-fill" style="width:' + pct.toFixed(1) + '%"></div></div>'
          +   '<div style="font-size:10px;color:#8b949e">' + pct.toFixed(0) + '%</div>'
          + '</td>'
          + '<td class="col-mem">' + formatBytes(r.gtt_bytes || 0) + '</td>'
          + '</tr>';
      } else {
        html += '<tr class="proc-empty"><td>&nbsp;</td><td class="col-pid"></td>'
          + '<td class="col-busy"><div class="bar"></div></td>'
          + '<td class="col-mem"></td></tr>';
      }
    }
    html += '</tbody>';
    table.innerHTML = html;
  }

  function renderNPUProcs(list) {
    var box = document.getElementById('npu-procs-list');
    if (!list || list.length === 0) {
      box.innerHTML = '';
      return;
    }
    var html = '';
    for (var i = 0; i < list.length; i++) {
      var p = list[i];
      html += '<div class="proc-row"><span>' + escapeHTML(p.comm || '?') + '</span>'
        + '<span class="proc-pid">PID ' + p.pid + '</span></div>';
    }
    box.innerHTML = html;
  }

  function escapeHTML(s) {
    return String(s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  function formatBytes(n) {
    if (!n) return '';
    if (n >= 1024 * 1024 * 1024) return (n / (1024 * 1024 * 1024)).toFixed(1) + ' GB';
    if (n >= 1024 * 1024) return (n / (1024 * 1024)).toFixed(0) + ' MB';
    if (n >= 1024) return (n / 1024).toFixed(0) + ' KB';
    return n + ' B';
  }

  // --- Per-core CPU details ---

  function setupCpuDetails() {
    if (coreOrder.length === 0) return;
    document.getElementById('cpu-details-wrap').style.display = '';
    // Reflect current showCores state on the per-core body. The header
    // toggle button drives changes after init.
    document.getElementById('cpu-details').style.display = showCores ? '' : 'none';

    // Build heatmap legend (only for types we actually have).
    var present = {};
    for (var i = 0; i < coreOrder.length; i++) present[coreTypes[coreOrder[i]]] = true;
    var legend = document.getElementById('cpu-heatmap-legend');
    legend.innerHTML = '';
    var types = [
      { key: 'performance', cls: 'lg-p',  label: TYPE_LABELS.performance },
      { key: 'efficient',   cls: 'lg-e',  label: TYPE_LABELS.efficient },
      { key: 'low_power',   cls: 'lg-lp', label: TYPE_LABELS.low_power }
    ];
    for (var ti = 0; ti < types.length; ti++) {
      if (!present[types[ti].key]) continue;
      var sp = document.createElement('span');
      sp.className = types[ti].cls;
      sp.textContent = types[ti].label;
      legend.appendChild(sp);
    }

    // Build chart legend.
    var chartLegend = document.getElementById('cpu-type-legend');
    chartLegend.innerHTML = '';
    for (var ti2 = 0; ti2 < types.length; ti2++) {
      if (!present[types[ti2].key]) continue;
      var sp2 = document.createElement('span');
      sp2.className = types[ti2].cls;
      sp2.textContent = types[ti2].label;
      chartLegend.appendChild(sp2);
    }
  }

  function drawPerCore() {
    if (coreOrder.length === 0) return;
    if (document.getElementById('cpu-details').style.display === 'none') return;
    drawHeatmap();
    drawTypeChart();
  }

  function drawHeatmap() {
    var canvas = document.getElementById('cpu-heatmap');
    if (!canvas) return;
    var ctx = canvas.getContext('2d');
    var dpr = window.devicePixelRatio || 1;
    var w = canvas.clientWidth, h = canvas.clientHeight;
    canvas.width = w * dpr; canvas.height = h * dpr;
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, w, h);

    // Group cores by type, preserving sorted order within each.
    var groups = { performance: [], efficient: [], low_power: [] };
    for (var i = 0; i < coreOrder.length; i++) {
      var id = coreOrder[i];
      var ct = coreTypes[id] || 'performance';
      if (!groups[ct]) groups[ct] = [];
      groups[ct].push(id);
    }
    var typeOrder = ['performance', 'efficient', 'low_power'];
    var presentTypes = typeOrder.filter(function(t) { return groups[t].length > 0; });
    if (presentTypes.length === 0) return;

    var rowH = Math.floor(h / presentTypes.length);
    var rowGap = 4;
    var labelW = 26;
    var cellGap = 2;

    ctx.font = '10px -apple-system, "Segoe UI", sans-serif';
    ctx.textBaseline = 'middle';

    for (var r = 0; r < presentTypes.length; r++) {
      var ct = presentTypes[r];
      var ids = groups[ct];
      var y = r * rowH;
      var cellH = rowH - rowGap;

      // Row label
      ctx.fillStyle = '#8b949e';
      var label = ct === 'performance' ? 'P' : (ct === 'efficient' ? 'E' : 'LP');
      ctx.fillText(label, 4, y + cellH / 2);

      // Cells
      var areaW = w - labelW;
      var cellW = (areaW - cellGap * (ids.length - 1)) / ids.length;
      for (var c = 0; c < ids.length; c++) {
        var id = ids[c];
        var arr = coreUtilHistory[id] || [];
        var v = arr.length > 0 ? arr[arr.length - 1].v : 0;
        var x = labelW + c * (cellW + cellGap);
        // Background
        ctx.fillStyle = '#0d1117';
        ctx.fillRect(x, y, cellW, cellH);
        // Fill: brightness scales with utilization, color = type
        var alpha = 0.15 + (v / 100) * 0.85;
        ctx.fillStyle = hexWithAlpha(TYPE_COLORS[ct], alpha);
        ctx.fillRect(x, y, cellW, cellH);
        // Optional value text if cell is wide enough
        if (cellW >= 22) {
          ctx.fillStyle = v > 50 ? '#ffffff' : '#8b949e';
          var txt = Math.round(v) + '';
          var tw = ctx.measureText(txt).width;
          ctx.fillText(txt, x + (cellW - tw) / 2, y + cellH / 2);
        }
      }
    }
  }

  function hexWithAlpha(hex, a) {
    var r = parseInt(hex.slice(1, 3), 16);
    var g = parseInt(hex.slice(3, 5), 16);
    var b = parseInt(hex.slice(5, 7), 16);
    return 'rgba(' + r + ',' + g + ',' + b + ',' + a + ')';
  }

  function drawTypeChart() {
    var canvas = document.getElementById('cpu-type-chart');
    if (!canvas) return;
    var ctx = canvas.getContext('2d');
    var dpr = window.devicePixelRatio || 1;
    var w = canvas.clientWidth, h = canvas.clientHeight;
    canvas.width = w * dpr; canvas.height = h * dpr;
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, w, h);

    // Gridlines
    ctx.strokeStyle = '#30363d';
    ctx.lineWidth = 0.5;
    for (var g = 0; g <= 4; g++) {
      var gy = (g / 4) * h;
      ctx.beginPath(); ctx.moveTo(0, gy); ctx.lineTo(w, gy); ctx.stroke();
    }

    var types = ['performance', 'efficient', 'low_power'];
    var slots = CHART_LEN - 1;
    var padY = 4, yRange = h - padY * 2;
    var min = 0, max = 100;

    for (var ti = 0; ti < types.length; ti++) {
      var tt = types[ti];
      var arr = typeAvgHistory[tt];
      if (!arr || arr.length < 2) continue;
      var startIdx = slots - (arr.length - 1);

      ctx.beginPath();
      ctx.moveTo((startIdx / slots) * w, h);
      for (var i = 0; i < arr.length; i++) {
        var x = ((startIdx + i) / slots) * w;
        var y = padY + yRange - ((arr[i].v - min) / (max - min)) * yRange;
        ctx.lineTo(x, y);
      }
      ctx.lineTo(w, h);
      ctx.closePath();
      ctx.fillStyle = TYPE_COLORS[tt] + '0d';
      ctx.fill();

      ctx.beginPath();
      for (var j = 0; j < arr.length; j++) {
        var x2 = ((startIdx + j) / slots) * w;
        var y2 = padY + yRange - ((arr[j].v - min) / (max - min)) * yRange;
        if (j === 0) ctx.moveTo(x2, y2); else ctx.lineTo(x2, y2);
      }
      ctx.strokeStyle = TYPE_COLORS[tt];
      ctx.lineWidth = 1.5;
      ctx.stroke();
    }
  }

  document.addEventListener('DOMContentLoaded', init);
})();
