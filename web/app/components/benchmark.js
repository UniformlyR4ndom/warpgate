import { html } from './html.js';
import { state } from '../store.js';
import { runDownloadBenchmark, runUploadBenchmark, clearBenchmarkResults } from '../lib/benchmark.js';
import { formatSize } from '../lib/util.js';

function formatDuration(ms) {
  return (ms / 1000).toFixed(2) + ' s';
}

function formatThroughput(bytes, ms) {
  if (!(ms > 0)) return '-';
  return ((bytes / (ms / 1000)) / (1024 * 1024)).toFixed(2) + ' MB/s';
}

function formatChunkFrame(r) {
  var parts = [];
  if (r.chunkBytes) { parts.push('chunk ' + formatSize(r.chunkBytes)); }
  if (r.frameBytes) { parts.push('frame ' + formatSize(r.frameBytes)); }
  return parts.length ? parts.join(' / ') : '-';
}

function Progress() {
  if (state.benchmarkPreparing) {
    var prep = state.benchmarkPrepProgress;
    return html`
      <div class="progress-caption ${prep === null ? 'progress-caption-pulse' : ''}">
        ${prep !== null ? 'Generating test data… ' + Math.round(prep * 100) + '%' : 'Hashing test data…'}
      </div>`;
  }
  var p = state.benchmarkProgress;
  return p !== null && html`
    <div class="progress-bar">
      <div class="progress-bar-fill" style=${'width:' + Math.round(p * 100) + '%'}></div>
      <span class="progress-bar-label">${Math.round(p * 100) + '%'}</span>
    </div>`;
}

export function BenchmarkTab() {
  var results = state.benchmarkResults;
  return html`
    <div>
      <div class="benchmark-actions">
        <button class="choose-btn" onClick=${runDownloadBenchmark} disabled=${state.benchmarkRunning}>Run Download Benchmark</button>
        <button class="choose-btn" onClick=${runUploadBenchmark} disabled=${state.benchmarkRunning}>Run Upload Benchmark</button>
        <button class="choose-btn" onClick=${clearBenchmarkResults} disabled=${results.length === 0}>Clear results</button>
      </div>
      <div class="bench-progress-wrap"><${Progress} /></div>
      ${results.length > 0 ? html`
        <table>
          <thead>
            <tr>
              <th>Direction</th>
              <th>Backend</th>
              <th class="col-size">Size</th>
              <th>Chunk/Frame</th>
              <th class="col-size">Duration</th>
              <th class="col-size">Throughput</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            ${results.map((r) => html`
              <tr key=${r.id} class="bench-row">
                <td>${r.direction === 'download' ? 'Download' : 'Upload'}</td>
                <td>${r.backend === 'ws' ? 'Websocket' : 'HTTP POST'}</td>
                <td class="col-size">${formatSize(r.sizeBytes)}</td>
                <td>${formatChunkFrame(r)}</td>
                <td class="col-size">${formatDuration(r.durationMs)}</td>
                <td class="col-size">${r.ok ? formatThroughput(r.sizeBytes, r.durationMs) : '-'}</td>
                <td class=${r.ok ? 'bench-ok' : 'bench-fail'}>${r.ok ? 'OK' : 'Failed: ' + r.error}</td>
              </tr>`)}
          </tbody>
        </table>` : html`<div class="upload-empty">No benchmark runs yet.</div>`}
    </div>`;
}
