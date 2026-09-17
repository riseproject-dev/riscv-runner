// SPDX-License-Identifier: MIT

package main

// weeklyUsageStyle defines the page chrome and the categorical palette used
// by both the pivot tables and the charts. Colors come from the project's
// validated categorical palette (dataviz skill, references/palette.md);
// light/dark values are the same eight hues stepped per surface, not a
// separate palette.
const weeklyUsageStyle = `<style>
:root {
  color-scheme: light;
  --page: #f9f9f7;
  --surface-1: #fcfcfb;
  --text-primary: #0b0b0b;
  --text-secondary: #52514e;
  --text-muted: #898781;
  --grid: #e1e0d9;
  --axis: #c3c2b7;
  --border: rgba(11,11,11,0.10);
  --series-1: #2a78d6;
  --series-2: #eb6834;
  --series-3: #1baf7a;
  --series-4: #eda100;
  --series-5: #e87ba4;
  --series-6: #008300;
  --series-7: #4a3aa7;
}
@media (prefers-color-scheme: dark) {
  :root {
    color-scheme: dark;
    --page: #0d0d0d;
    --surface-1: #1a1a19;
    --text-primary: #ffffff;
    --text-secondary: #c3c2b7;
    --text-muted: #898781;
    --grid: #2c2c2a;
    --axis: #383835;
    --border: rgba(255,255,255,0.10);
    --series-1: #3987e5;
    --series-2: #d95926;
    --series-3: #199e70;
    --series-4: #c98500;
    --series-5: #d55181;
    --series-6: #008300;
    --series-7: #9085e9;
  }
}
* { box-sizing: border-box; }
body {
  background: var(--page);
  color: var(--text-primary);
  font-family: system-ui, -apple-system, "Segoe UI", sans-serif;
  margin: 0;
  padding: 24px;
}
h1 { font-size: 22px; margin: 0 0 16px; }
h2 { font-size: 16px; margin: 28px 0 8px; color: var(--text-primary); }
h3 { font-size: 15px; margin: 0 0 4px; }
.table-scroll {
  overflow-x: auto;
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 8px;
}
table.pivot {
  border-collapse: collapse;
  width: 100%;
  font-size: 13px;
  font-variant-numeric: tabular-nums;
}
table.pivot th, table.pivot td {
  padding: 6px 10px;
  text-align: right;
  white-space: nowrap;
  border-bottom: 1px solid var(--grid);
}
table.pivot thead th {
  text-align: right;
  color: var(--text-secondary);
  font-weight: 600;
  border-bottom: 1px solid var(--axis);
}
table.pivot th:first-child, table.pivot td:first-child {
  text-align: left;
  color: var(--text-secondary);
}
table.pivot tbody tr:hover { background: var(--grid); }
table.pivot .total-row { font-weight: 600; border-top: 1px solid var(--axis); }
table.pivot .total-row th { text-align: left; }
table.pivot td.total-col, table.pivot th.total-col { border-left: 1px solid var(--axis); }
table.pivot-merged td.group-start, table.pivot-merged th.group-start { border-left: 1px solid var(--axis); }
.chart-grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: 16px;
  margin-top: 8px;
}
.chart-card {
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 16px;
}
.chart-caption {
  font-size: 12px;
  color: var(--text-muted);
  margin: 0 0 8px;
}
.chart-inner { position: relative; }
.chart-svg { width: 100%; height: auto; display: block; overflow: visible; }
.grid-line { stroke: var(--grid); stroke-width: 1; }
.axis-line { stroke: var(--axis); stroke-width: 1; }
.axis-line-color { stroke-width: 2; }
.axis-label { fill: var(--text-muted); font-size: 10px; }
.line-stroke { stroke: var(--sc); stroke-width: 2; fill: none; stroke-linecap: round; stroke-linejoin: round; }
.area-fill { fill: var(--sc); fill-opacity: 0.10; stroke: none; }
.end-dot { fill: var(--sc); stroke: var(--surface-1); stroke-width: 2; }
.end-label { fill: var(--text-secondary); font-size: 11px; }
.crosshair { stroke: var(--axis); stroke-width: 1; pointer-events: none; }
.hit-rect { cursor: crosshair; }
.s1 { --sc: var(--series-1); }
.s2 { --sc: var(--series-2); }
.s3 { --sc: var(--series-3); }
.s4 { --sc: var(--series-4); }
.s5 { --sc: var(--series-5); }
.s6 { --sc: var(--series-6); }
.s7 { --sc: var(--series-7); }
.s-other { --sc: var(--text-muted); }
.chart-legend {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  margin-top: 8px;
  font-size: 12px;
  color: var(--text-secondary);
}
.legend-item { display: inline-flex; align-items: center; gap: 6px; }
.legend-swatch { width: 12px; height: 3px; border-radius: 2px; background: var(--sc); display: inline-block; }
.chart-tooltip {
  position: absolute;
  top: 8px;
  transform: translateX(-8px);
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 8px 10px;
  font-size: 12px;
  box-shadow: 0 2px 8px rgba(0,0,0,0.15);
  pointer-events: none;
  min-width: 140px;
}
.tooltip-header { font-weight: 600; margin-bottom: 4px; color: var(--text-primary); }
.tooltip-row { display: flex; align-items: center; gap: 6px; padding: 1px 0; }
.tooltip-key { width: 10px; height: 3px; border-radius: 2px; background: var(--sc); display: inline-block; flex: none; }
.tooltip-value { font-weight: 600; font-variant-numeric: tabular-nums; color: var(--text-primary); }
.tooltip-name { color: var(--text-secondary); }
</style>
`

// weeklyUsageChartsMarkup is the static chart-card scaffold. weeklyUsageScript
// mounts each chart's SVG into the matching #chart-* div.
const weeklyUsageChartsMarkup = `<h2>Charts</h2>
<div class="chart-grid">
<section class="chart-card">
<h3>Total duration by entity</h3>
<p class="chart-caption">Stacked weekly total_duration_minutes per entity_name. Exact values: Weekly usage table above.</p>
<div class="chart-mount" id="chart-all"></div>
</section>
<section class="chart-card">
<h3>Total duration by entity (excluding riseproject-dev)</h3>
<p class="chart-caption">Same as above with entity_name = riseproject-dev removed.</p>
<div class="chart-mount" id="chart-ex-riseproject-dev"></div>
</section>
<section class="chart-card">
<h3>Weekly totals: duration and job count</h3>
<p class="chart-caption">SUM(total_duration_minutes) and SUM(job_count) per week across all entities, separate y-axes.</p>
<div class="chart-mount" id="chart-totals"></div>
</section>
</div>
`

// weeklyUsageScript renders the three charts from the JSON data island using
// hand-rolled SVG (no CDN dependency, so the page works on an internal-only
// network). Entity names are untrusted (GitHub org/webhook data) so they're
// only ever inserted via textContent, never innerHTML.
const weeklyUsageScript = `<script>
(function () {
  var dataEl = document.getElementById('weekly-usage-data');
  if (!dataEl) return;
  var data = JSON.parse(dataEl.textContent);

  var WIDTH = 900, HEIGHT = 320;

  function niceMax(max) {
    if (max <= 0) return 1;
    var exp = Math.floor(Math.log10(max));
    var base = Math.pow(10, exp);
    var f = max / base;
    var niceF = f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10;
    return niceF * base;
  }

  function ticksFor(max, count) {
    var step = max / count, out = [];
    for (var i = 0; i <= count; i++) out.push(Math.round(step * i));
    return out;
  }

  function fmtNumber(n) { return n.toLocaleString('en-US'); }

  // fmtDuration mirrors the Go formatDuration in weekly_usage.go: "X days H
  // hours M minutes", dropping leading zero units.
  function fmtDuration(totalMinutes) {
    var n = Math.round(totalMinutes);
    if (n === 0) return '0 minutes';
    var neg = n < 0;
    if (neg) n = -n;
    var days = Math.floor(n / 1440);
    var hours = Math.floor(n / 60) % 24;
    var minutes = n % 60;
    var parts = [];
    if (days > 0) parts.push(days + (days === 1 ? ' day' : ' days'));
    if (days > 0 || hours > 0) parts.push(hours + (hours === 1 ? ' hour' : ' hours'));
    parts.push(minutes + (minutes === 1 ? ' minute' : ' minutes'));
    return (neg ? '-' : '') + parts.join(' ');
  }

  // fmtSeriesValue formats a series' value for its tooltip row: minutes as
  // "X days H hours M minutes" for duration series, a plain comma'd number
  // for count series (job counts). Axis ticks stay in plain numbers (see
  // buildChart/buildDualAxisChart) since a full duration string doesn't fit
  // the tick margin.
  function fmtSeriesValue(series, n) {
    return series.unit === 'count' ? fmtNumber(n) : fmtDuration(n);
  }

  function svgEl(tag, attrs) {
    var el = document.createElementNS('http://www.w3.org/2000/svg', tag);
    for (var k in attrs) el.setAttribute(k, attrs[k]);
    return el;
  }

  function weekIndexTicks(weeks) {
    var n = weeks.length, maxLabels = 8;
    var stride = Math.max(1, Math.ceil(n / maxLabels));
    var idx = [];
    for (var i = 0; i < n; i += stride) idx.push(i);
    var last = idx[idx.length - 1];
    if (last !== n - 1) {
      // A forced final tick too close to the previous one collides with it;
      // replace rather than append in that case.
      if (n - 1 - last <= stride / 2) idx.pop();
      idx.push(n - 1);
    }
    return idx;
  }

  function buildLegend(series) {
    var legend = document.createElement('div');
    legend.className = 'chart-legend';
    series.forEach(function (s) {
      var item = document.createElement('span');
      item.className = 'legend-item';
      var swatch = document.createElement('span');
      swatch.className = 'legend-swatch ' + s.class;
      var label = document.createElement('span');
      label.className = 'legend-label';
      label.textContent = s.name;
      item.appendChild(swatch);
      item.appendChild(label);
      legend.appendChild(item);
    });
    return legend;
  }

  // attachHover wires a shared crosshair + all-series tooltip to hitRect,
  // reused by both the stacked and dual-axis charts.
  function attachHover(hitRect, svg, marginLeft, innerW, x, crosshair, tooltip, weeks, series) {
    function update(evt) {
      var rect = svg.getBoundingClientRect();
      var px = (evt.clientX - rect.left) / rect.width * WIDTH - marginLeft;
      var idx = Math.round((px / innerW) * (weeks.length - 1));
      idx = Math.max(0, Math.min(weeks.length - 1, idx));

      crosshair.setAttribute('visibility', 'visible');
      crosshair.setAttribute('x1', x(idx));
      crosshair.setAttribute('x2', x(idx));

      tooltip.innerHTML = '';
      var header = document.createElement('div');
      header.className = 'tooltip-header';
      header.textContent = weeks[idx];
      tooltip.appendChild(header);
      for (var s = 0; s < series.length; s++) {
        var sr = series[s];
        var row = document.createElement('div');
        row.className = 'tooltip-row';
        var key = document.createElement('span');
        key.className = 'tooltip-key ' + sr.class;
        var val = document.createElement('span');
        val.className = 'tooltip-value';
        val.textContent = fmtSeriesValue(sr, sr.values[idx]);
        var name = document.createElement('span');
        name.className = 'tooltip-name';
        name.textContent = sr.name;
        row.appendChild(key);
        row.appendChild(val);
        row.appendChild(name);
        tooltip.appendChild(row);
      }
      tooltip.style.display = 'block';
      tooltip.style.left = Math.min(80, Math.max(0, (x(idx) / innerW) * 100)) + '%';
    }
    hitRect.addEventListener('pointermove', update);
    hitRect.addEventListener('pointerleave', function () {
      crosshair.setAttribute('visibility', 'hidden');
      tooltip.style.display = 'none';
    });
  }

  function buildChart(mount, weeks, series) {
    var margin = { top: 16, right: 24, bottom: 32, left: 56 };
    var innerW = WIDTH - margin.left - margin.right;
    var innerH = HEIGHT - margin.top - margin.bottom;
    var n = weeks.length;

    var cum = [], maxVal = 0;
    for (var i = 0; i < n; i++) {
      var running = 0, row = [0];
      for (var s = 0; s < series.length; s++) {
        running += series[s].values[i];
        row.push(running);
      }
      cum.push(row);
      if (running > maxVal) maxVal = running;
    }
    var yMax = niceMax(maxVal || 1);
    var yTicks = ticksFor(yMax, 4);

    function x(i) { return n <= 1 ? innerW / 2 : (i / (n - 1)) * innerW; }
    function y(v) { return innerH - (v / yMax) * innerH; }

    var svg = svgEl('svg', { viewBox: '0 0 ' + WIDTH + ' ' + HEIGHT, class: 'chart-svg', role: 'img' });
    var g = svgEl('g', { transform: 'translate(' + margin.left + ',' + margin.top + ')' });
    svg.appendChild(g);

    yTicks.forEach(function (t) {
      var yy = y(t);
      g.appendChild(svgEl('line', { x1: 0, x2: innerW, y1: yy, y2: yy, class: 'grid-line' }));
      var lbl = svgEl('text', { x: -8, y: yy, class: 'axis-label', 'text-anchor': 'end', 'dominant-baseline': 'middle' });
      lbl.textContent = fmtNumber(t);
      g.appendChild(lbl);
    });
    g.appendChild(svgEl('line', { x1: 0, x2: 0, y1: 0, y2: innerH, class: 'axis-line' }));
    g.appendChild(svgEl('line', { x1: 0, x2: innerW, y1: innerH, y2: innerH, class: 'axis-line' }));

    weekIndexTicks(weeks).forEach(function (i) {
      var lbl = svgEl('text', { x: x(i), y: innerH + 18, class: 'axis-label', 'text-anchor': 'middle' });
      lbl.textContent = weeks[i];
      g.appendChild(lbl);
    });

    for (var s = series.length - 1; s >= 0; s--) {
      var top = cum.map(function (row) { return row[s + 1]; });
      var bottom = cum.map(function (row) { return row[s]; });
      var d = 'M ' + x(0) + ' ' + y(bottom[0]);
      for (var i = 0; i < n; i++) d += ' L ' + x(i) + ' ' + y(top[i]);
      for (var i = n - 1; i >= 0; i--) d += ' L ' + x(i) + ' ' + y(bottom[i]);
      d += ' Z';
      g.appendChild(svgEl('path', { d: d, class: 'area-fill ' + series[s].class }));
    }
    for (var s = 0; s < series.length; s++) {
      var top = cum.map(function (row) { return row[s + 1]; });
      var d = 'M ' + x(0) + ' ' + y(top[0]);
      for (var i = 1; i < n; i++) d += ' L ' + x(i) + ' ' + y(top[i]);
      g.appendChild(svgEl('path', { d: d, class: 'line-stroke ' + series[s].class }));
    }

    var crosshair = svgEl('line', { x1: 0, x2: 0, y1: 0, y2: innerH, class: 'crosshair', visibility: 'hidden' });
    g.appendChild(crosshair);
    var hitRect = svgEl('rect', { x: 0, y: 0, width: innerW, height: innerH, class: 'hit-rect', fill: 'transparent' });
    g.appendChild(hitRect);

    var wrap = document.createElement('div');
    wrap.className = 'chart-inner';
    wrap.appendChild(svg);
    var tooltip = document.createElement('div');
    tooltip.className = 'chart-tooltip';
    tooltip.style.display = 'none';
    wrap.appendChild(tooltip);
    wrap.appendChild(buildLegend(series));
    mount.appendChild(wrap);

    attachHover(hitRect, svg, margin.left, innerW, x, crosshair, tooltip, weeks, series);
  }

  function buildDualAxisChart(mount, weeks, left, right) {
    var margin = { top: 16, right: 64, bottom: 32, left: 64 };
    var innerW = WIDTH - margin.left - margin.right;
    var innerH = HEIGHT - margin.top - margin.bottom;
    var n = weeks.length;

    var leftMax = niceMax(Math.max.apply(null, left.values.concat([1])));
    var rightMax = niceMax(Math.max.apply(null, right.values.concat([1])));
    var leftTicks = ticksFor(leftMax, 4);
    var rightTicks = ticksFor(rightMax, 4);

    function x(i) { return n <= 1 ? innerW / 2 : (i / (n - 1)) * innerW; }
    function yLeft(v) { return innerH - (v / leftMax) * innerH; }
    function yRight(v) { return innerH - (v / rightMax) * innerH; }

    var svg = svgEl('svg', { viewBox: '0 0 ' + WIDTH + ' ' + HEIGHT, class: 'chart-svg', role: 'img' });
    var g = svgEl('g', { transform: 'translate(' + margin.left + ',' + margin.top + ')' });
    svg.appendChild(g);

    leftTicks.forEach(function (t) {
      var yy = yLeft(t);
      g.appendChild(svgEl('line', { x1: 0, x2: innerW, y1: yy, y2: yy, class: 'grid-line' }));
      var lbl = svgEl('text', { x: -8, y: yy, class: 'axis-label', 'text-anchor': 'end', 'dominant-baseline': 'middle' });
      lbl.textContent = fmtNumber(t);
      g.appendChild(lbl);
    });
    rightTicks.forEach(function (t) {
      var yy = yRight(t);
      var lbl = svgEl('text', { x: innerW + 8, y: yy, class: 'axis-label', 'text-anchor': 'start', 'dominant-baseline': 'middle' });
      lbl.textContent = fmtNumber(t);
      g.appendChild(lbl);
    });
    g.appendChild(svgEl('line', { x1: 0, x2: 0, y1: 0, y2: innerH, class: 'axis-line axis-line-color ' + left.class }));
    g.appendChild(svgEl('line', { x1: innerW, x2: innerW, y1: 0, y2: innerH, class: 'axis-line axis-line-color ' + right.class }));
    g.appendChild(svgEl('line', { x1: 0, x2: innerW, y1: innerH, y2: innerH, class: 'axis-line' }));

    weekIndexTicks(weeks).forEach(function (i) {
      var lbl = svgEl('text', { x: x(i), y: innerH + 18, class: 'axis-label', 'text-anchor': 'middle' });
      lbl.textContent = weeks[i];
      g.appendChild(lbl);
    });

    function drawLine(series, yFn) {
      var d = 'M ' + x(0) + ' ' + yFn(series.values[0]);
      for (var i = 1; i < n; i++) d += ' L ' + x(i) + ' ' + yFn(series.values[i]);
      g.appendChild(svgEl('path', { d: d, class: 'line-stroke ' + series.class }));
      var last = n - 1;
      g.appendChild(svgEl('circle', { cx: x(last), cy: yFn(series.values[last]), r: 4, class: 'end-dot ' + series.class }));
      var label = svgEl('text', { x: x(last) - 4, y: yFn(series.values[last]) - 10, class: 'end-label', 'text-anchor': 'end' });
      label.textContent = fmtSeriesValue(series, series.values[last]);
      g.appendChild(label);
    }
    drawLine(left, yLeft);
    drawLine(right, yRight);

    var crosshair = svgEl('line', { x1: 0, x2: 0, y1: 0, y2: innerH, class: 'crosshair', visibility: 'hidden' });
    g.appendChild(crosshair);
    var hitRect = svgEl('rect', { x: 0, y: 0, width: innerW, height: innerH, class: 'hit-rect', fill: 'transparent' });
    g.appendChild(hitRect);

    var wrap = document.createElement('div');
    wrap.className = 'chart-inner';
    wrap.appendChild(svg);
    var tooltip = document.createElement('div');
    tooltip.className = 'chart-tooltip';
    tooltip.style.display = 'none';
    wrap.appendChild(tooltip);
    wrap.appendChild(buildLegend([left, right]));
    mount.appendChild(wrap);

    attachHover(hitRect, svg, margin.left, innerW, x, crosshair, tooltip, weeks, [left, right]);
  }

  var chartAll = document.getElementById('chart-all');
  if (chartAll) buildChart(chartAll, data.weeks, data.durationByEntity);
  var chartEx = document.getElementById('chart-ex-riseproject-dev');
  if (chartEx) buildChart(chartEx, data.weeks, data.durationByEntityExRiseprojectDev);
  var chartTotals = document.getElementById('chart-totals');
  if (chartTotals) {
    buildDualAxisChart(chartTotals, data.weeks,
      { name: 'Total duration', class: 's1', unit: 'duration', values: data.totalDuration },
      { name: 'Total jobs', class: 's2', unit: 'count', values: data.totalJobs });
  }
})();
</script>
`
