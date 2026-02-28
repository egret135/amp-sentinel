// Amp Sentinel Dashboard
(function() {
'use strict';

const API_BASE = '/admin/v1';
const STORAGE_KEY = 'amp_sentinel_token';
const REFRESH_MS = 15000;
const PAGE_SIZE = 20;

let token = localStorage.getItem(STORAGE_KEY) || '';
let refreshTimer = null;
let statusChart = null;
let tokensChart = null;
let currentPage = 'dashboard';
let projects = [];
let incOffset = 0;
let taskOffset = 0;

// ── API ──

async function api(path, opts = {}) {
    const headers = { 'Content-Type': 'application/json' };
    if (token) headers['Authorization'] = 'Bearer ' + token;
    const resp = await fetch(API_BASE + path, { ...opts, headers });
    if (resp.status === 401) { logout(); throw new Error('认证失败'); }
    if (!resp.ok) {
        const b = await resp.json().catch(() => ({}));
        throw new Error(b.error || 'HTTP ' + resp.status);
    }
    return resp.json();
}

// ── Auth ──

function checkAuth() { token ? showApp() : showLogin(); }

function showLogin() {
    document.getElementById('login-screen').classList.remove('hidden');
    document.getElementById('app').classList.add('hidden');
    stopRefresh();
}

function showApp() {
    document.getElementById('login-screen').classList.add('hidden');
    document.getElementById('app').classList.remove('hidden');
    navigate(location.hash.slice(1) || 'dashboard');
    startRefresh();
    loadProjects();
    checkHealth();
}

function logout() {
    token = '';
    localStorage.removeItem(STORAGE_KEY);
    showLogin();
}

document.getElementById('login-btn').addEventListener('click', async () => {
    const input = document.getElementById('login-token');
    const errEl = document.getElementById('login-error');
    const val = input.value.trim();
    if (!val) { errEl.textContent = '请输入 Token'; errEl.classList.remove('hidden'); return; }
    token = val;
    try {
        await api('/stats');
        localStorage.setItem(STORAGE_KEY, token);
        errEl.classList.add('hidden');
        showApp();
    } catch (e) {
        token = '';
        errEl.textContent = '认证失败：' + e.message;
        errEl.classList.remove('hidden');
    }
});

document.getElementById('login-token').addEventListener('keydown', e => {
    if (e.key === 'Enter') document.getElementById('login-btn').click();
});

document.getElementById('logout-btn').addEventListener('click', logout);

// ── Navigation ──

function navigate(page) {
    if (!page) page = 'dashboard';
    currentPage = page;
    document.querySelectorAll('.nav-link').forEach(l => {
        const active = l.dataset.page === page;
        l.classList.toggle('bg-blue-50', active);
        l.classList.toggle('text-blue-600', active);
        l.classList.toggle('font-medium', active);
    });
    document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
    const el = document.getElementById('page-' + page);
    if (el) el.classList.add('active');
    switch (page) {
        case 'dashboard': loadDashboard(); break;
        case 'incidents': loadIncidents(); break;
        case 'tasks': loadTasks(); break;
        case 'projects': loadProjectsPage(); break;
    }
}

window.addEventListener('hashchange', () => navigate(location.hash.slice(1)));

// ── Health ──

async function checkHealth() {
    try {
        await api('/health');
        document.getElementById('health-status').innerHTML = '<span class="text-emerald-500">● 已连接</span>';
    } catch {
        document.getElementById('health-status').innerHTML = '<span class="text-red-500">● 连接失败</span>';
    }
}

// ── Projects ──

async function loadProjects() {
    try {
        projects = await api('/projects') || [];
        populateProjectFilters();
    } catch (e) { console.error('load projects:', e); }
}

function populateProjectFilters() {
    ['filter-inc-project', 'filter-task-project'].forEach(id => {
        const sel = document.getElementById(id);
        if (!sel) return;
        while (sel.options.length > 1) sel.remove(1);
        projects.forEach(p => {
            const o = document.createElement('option');
            o.value = p.key;
            o.textContent = p.name || p.key;
            sel.appendChild(o);
        });
    });
}

// ── Dashboard ──

async function loadDashboard() {
    try {
        const [stats, incidents] = await Promise.all([api('/stats'), api('/incidents?limit=10')]);
        const u = stats.usage || {};
        const s = stats.scheduler || {};
        const bs = u.tasks_by_status || {};

        setText('stat-total-incidents', u.total_incidents || 0);
        setText('stat-today-incidents', u.today_incidents || 0);
        setText('stat-running', s.running || 0);
        setText('stat-queue', s.queue_length || 0);

        const completed = bs.completed || 0;
        const failed = bs.failed || 0;
        setText('stat-completed', completed);
        setText('stat-failed', failed);
        setText('stat-timeout', bs.timeout || 0);

        const total = Object.values(bs).reduce((a, b) => a + b, 0);
        setText('stat-success-rate', total > 0 ? Math.round(completed / total * 100) + '%' : '-');

        renderStatusChart(bs);
        renderTokensChart(u.total_input_tokens || 0, u.total_output_tokens || 0);
        renderRecentIncidents(incidents || []);
    } catch (e) { console.error('dashboard:', e); }
}

function renderStatusChart(bs) {
    const ctx = document.getElementById('chart-status');
    if (!ctx) return;
    const data = [bs.completed||0, bs.failed||0, bs.running||0, bs.pending||0, bs.timeout||0];
    if (statusChart) { statusChart.data.datasets[0].data = data; statusChart.update(); return; }
    statusChart = new Chart(ctx, {
        type: 'doughnut',
        data: {
            labels: ['已完成','失败','运行中','等待中','超时'],
            datasets: [{ data, backgroundColor: ['#10b981','#ef4444','#f59e0b','#3b82f6','#f97316'], borderWidth: 0 }]
        },
        options: {
            responsive: true, maintainAspectRatio: false, cutout: '60%',
            plugins: { legend: { position: 'right', labels: { color: '#64748b', padding: 12, usePointStyle: true } } }
        }
    });
}

function renderTokensChart(input, output) {
    const ctx = document.getElementById('chart-tokens');
    if (!ctx) return;
    if (tokensChart) { tokensChart.data.datasets[0].data = [input, output]; tokensChart.update(); return; }
    tokensChart = new Chart(ctx, {
        type: 'bar',
        data: {
            labels: ['Input Tokens', 'Output Tokens'],
            datasets: [{ data: [input, output], backgroundColor: ['#3b82f6','#8b5cf6'], borderWidth: 0, borderRadius: 6, barPercentage: 0.5 }]
        },
        options: {
            responsive: true, maintainAspectRatio: false,
            plugins: { legend: { display: false }, tooltip: { callbacks: { label: c => fmtNum(c.raw) } } },
            scales: {
                y: { beginAtZero: true, grid: { color: '#e2e8f0' }, ticks: { color: '#94a3b8', callback: v => fmtNum(v) } },
                x: { grid: { display: false }, ticks: { color: '#94a3b8' } }
            }
        }
    });
}

function renderRecentIncidents(list) {
    const el = document.getElementById('recent-incidents');
    if (!list.length) { el.innerHTML = '<div class="text-slate-400 text-sm py-4 text-center">暂无故障事件</div>'; return; }
    el.innerHTML = list.map(i => `
        <div class="flex items-center justify-between py-2 px-3 rounded-lg hover:bg-blue-50 cursor-pointer transition-colors" onclick="showIncidentDetail('${i.id}')">
            <div class="flex items-center gap-3 min-w-0">
                ${sevBadge(i.severity)}
                <span class="text-sm truncate text-slate-700">${esc(i.title||i.error_msg||i.id)}</span>
            </div>
            <div class="flex items-center gap-3 text-xs text-slate-400 flex-shrink-0 ml-3">
                <span>${esc(i.project_key)}</span>
                <span>${fmtTime(i.occurred_at)}</span>
            </div>
        </div>`).join('');
}

// ── Incidents ──

async function loadIncidents(offset) {
    if (offset === undefined) offset = incOffset;
    incOffset = offset;
    const p = new URLSearchParams({ limit: PAGE_SIZE, offset });
    const pv = document.getElementById('filter-inc-project')?.value;
    const sv = document.getElementById('filter-inc-severity')?.value;
    if (pv) p.set('project_key', pv);
    if (sv) p.set('severity', sv);
    try {
        const list = await api('/incidents?' + p) || [];
        const tb = document.getElementById('incidents-tbody');
        if (!list.length) {
            tb.innerHTML = '<tr><td colspan="7" class="px-4 py-8 text-center text-slate-400">暂无数据</td></tr>';
        } else {
            tb.innerHTML = list.map(i => `
                <tr class="hover:bg-blue-50/50 transition-colors">
                    <td class="px-4 py-3 text-xs font-mono text-slate-400">${esc(i.id)}</td>
                    <td class="px-4 py-3 text-sm max-w-xs truncate text-slate-700">${esc(i.title||i.error_msg||'-')}</td>
                    <td class="px-4 py-3 text-sm text-slate-600">${esc(i.project_key)}</td>
                    <td class="px-4 py-3">${sevBadge(i.severity)}</td>
                    <td class="px-4 py-3 text-sm text-slate-400">${esc(i.source||'-')}</td>
                    <td class="px-4 py-3 text-sm text-slate-400">${fmtTime(i.occurred_at)}</td>
                    <td class="px-4 py-3">
                        <div class="flex gap-1">
                            <button onclick="showIncidentDetail('${i.id}')" class="text-blue-600 hover:text-blue-700 text-xs px-2 py-1 rounded hover:bg-blue-50">详情</button>
                            <button onclick="retryIncident('${i.id}')" class="text-orange-500 hover:text-orange-600 text-xs px-2 py-1 rounded hover:bg-orange-50">重试</button>
                        </div>
                    </td>
                </tr>`).join('');
        }
        renderPagination('incidents-pagination', list.length, offset, 'incidents');
    } catch (e) {
        document.getElementById('incidents-tbody').innerHTML = `<tr><td colspan="7" class="px-4 py-8 text-center text-red-500">${esc(e.message)}</td></tr>`;
    }
}

document.getElementById('filter-inc-project')?.addEventListener('change', () => loadIncidents(0));
document.getElementById('filter-inc-severity')?.addEventListener('change', () => loadIncidents(0));

// ── Tasks ──

async function loadTasks(offset) {
    if (offset === undefined) offset = taskOffset;
    taskOffset = offset;
    const p = new URLSearchParams({ limit: PAGE_SIZE, offset });
    const pv = document.getElementById('filter-task-project')?.value;
    const sv = document.getElementById('filter-task-status')?.value;
    if (pv) p.set('project_key', pv);
    if (sv) p.set('status', sv);
    try {
        const list = await api('/tasks?' + p) || [];
        const tb = document.getElementById('tasks-tbody');
        if (!list.length) {
            tb.innerHTML = '<tr><td colspan="8" class="px-4 py-8 text-center text-slate-400">暂无数据</td></tr>';
        } else {
            tb.innerHTML = list.map(t => `
                <tr class="hover:bg-blue-50/50 transition-colors">
                    <td class="px-4 py-3 text-xs font-mono text-slate-400">${esc(t.id)}</td>
                    <td class="px-4 py-3 text-xs font-mono text-slate-400">${esc(t.incident_id)}</td>
                    <td class="px-4 py-3 text-sm text-slate-600">${esc(t.project_key)}</td>
                    <td class="px-4 py-3">${statusBadge(t.status)}</td>
                    <td class="px-4 py-3 text-sm text-slate-500">${t.duration_ms ? (t.duration_ms/1000).toFixed(1)+'s' : '-'}</td>
                    <td class="px-4 py-3 text-sm text-slate-500">${t.input_tokens||t.output_tokens ? fmtNum(t.input_tokens)+'↓ '+fmtNum(t.output_tokens)+'↑' : '-'}</td>
                    <td class="px-4 py-3 text-sm text-slate-400">${fmtTime(t.created_at)}</td>
                    <td class="px-4 py-3">
                        <button onclick="showTaskDetail('${t.id}')" class="text-blue-600 hover:text-blue-700 text-xs px-2 py-1 rounded hover:bg-blue-50">详情</button>
                    </td>
                </tr>`).join('');
        }
        renderPagination('tasks-pagination', list.length, offset, 'tasks');
    } catch (e) {
        document.getElementById('tasks-tbody').innerHTML = `<tr><td colspan="8" class="px-4 py-8 text-center text-red-500">${esc(e.message)}</td></tr>`;
    }
}

document.getElementById('filter-task-project')?.addEventListener('change', () => loadTasks(0));
document.getElementById('filter-task-status')?.addEventListener('change', () => loadTasks(0));

// ── Projects Page ──

async function loadProjectsPage() {
    const el = document.getElementById('projects-grid');
    try {
        const data = projects.length ? projects : (await api('/projects') || []);
        if (!data.length) { el.innerHTML = '<div class="text-slate-400 col-span-full text-center py-8">暂未注册项目</div>'; return; }
        el.innerHTML = data.map(p => `
            <div class="bg-white rounded-xl p-5 border border-slate-200 hover:border-blue-300 hover:shadow-md transition-all shadow-sm">
                <div class="flex items-center justify-between mb-3">
                    <h3 class="font-bold text-lg text-slate-800">${esc(p.name||p.key)}</h3>
                    ${p.has_webhook ? '<span class="text-emerald-500 text-xs">● Webhook</span>' : '<span class="text-slate-400 text-xs">○ No Webhook</span>'}
                </div>
                <div class="space-y-2 text-sm">
                    <div class="flex justify-between"><span class="text-slate-400">Key</span><span class="font-mono text-slate-600">${esc(p.key)}</span></div>
                    <div class="flex justify-between"><span class="text-slate-400">语言</span><span class="text-slate-600">${esc(p.language||'-')}</span></div>
                    <div class="flex justify-between"><span class="text-slate-400">分支</span><span class="font-mono text-slate-600">${esc(p.branch||'main')}</span></div>
                    ${p.owners?.length ? `<div class="flex justify-between"><span class="text-slate-400">负责人</span><span class="text-slate-600">${p.owners.map(o=>esc(o)).join(', ')}</span></div>` : ''}
                    ${p.skills?.length ? `<div class="pt-2 border-t border-slate-100"><span class="text-slate-400 text-xs">Skills:</span><div class="flex flex-wrap gap-1 mt-1">${p.skills.map(s=>`<span class="bg-blue-50 text-blue-600 text-xs px-2 py-0.5 rounded-full">${esc(s)}</span>`).join('')}</div></div>` : ''}
                </div>
            </div>`).join('');
    } catch (e) { el.innerHTML = `<div class="text-red-500 col-span-full">${esc(e.message)}</div>`; }
}

// ── Modals ──

window.closeModal = function(id) {
    document.getElementById(id).classList.add('hidden');
    if (id === 'modal-task' && taskDetailTimer) { clearInterval(taskDetailTimer); taskDetailTimer = null; }
};

let taskDetailTimer = null;

document.addEventListener('keydown', e => {
    if (e.key === 'Escape') {
        // Confirm dialog: treat as cancel
        const confirmModal = document.getElementById('modal-confirm');
        if (confirmModal && !confirmModal.classList.contains('hidden')) {
            document.getElementById('confirm-cancel').click();
            return;
        }
        ['modal-report', 'modal-incident', 'modal-task'].forEach(id => {
            const el = document.getElementById(id);
            if (el && !el.classList.contains('hidden')) {
                el.classList.add('hidden');
                if (id === 'modal-task' && taskDetailTimer) { clearInterval(taskDetailTimer); taskDetailTimer = null; }
            }
        });
    }
});

window.showIncidentDetail = async function(id) {
    const modal = document.getElementById('modal-incident');
    const content = document.getElementById('modal-incident-content');
    modal.classList.remove('hidden');
    content.innerHTML = '<div class="text-slate-400">加载中...</div>';
    try {
        const i = await api('/incidents/' + id);
        content.innerHTML = `
            <div class="space-y-4">
                <div class="grid grid-cols-2 gap-4">
                    <div><span class="text-slate-400 text-sm">ID</span><div class="font-mono text-sm text-slate-700">${esc(i.id)}</div></div>
                    <div><span class="text-slate-400 text-sm">项目</span><div class="text-slate-700">${esc(i.project_key)}</div></div>
                    <div><span class="text-slate-400 text-sm">严重度</span><div>${sevBadge(i.severity)}</div></div>
                    <div><span class="text-slate-400 text-sm">环境</span><div class="text-slate-700">${esc(i.environment||'-')}</div></div>
                    <div><span class="text-slate-400 text-sm">来源</span><div class="text-slate-700">${esc(i.source||'-')}</div></div>
                    <div><span class="text-slate-400 text-sm">发生时间</span><div class="text-slate-700">${fmtTime(i.occurred_at)}</div></div>
                </div>
                <div><span class="text-slate-400 text-sm">标题</span><div class="mt-1 text-slate-800 font-medium">${esc(i.title||'-')}</div></div>
                ${i.error_msg ? `<div><span class="text-slate-400 text-sm">错误信息</span><pre class="mt-1 bg-red-50 border border-red-100 rounded-lg p-3 text-sm text-red-600 overflow-x-auto whitespace-pre-wrap">${esc(i.error_msg)}</pre></div>` : ''}
                ${i.stacktrace ? `<div><span class="text-slate-400 text-sm">堆栈跟踪</span><pre class="mt-1 bg-slate-50 border border-slate-200 rounded-lg p-3 text-xs text-slate-600 overflow-x-auto max-h-64 overflow-y-auto">${esc(i.stacktrace)}</pre></div>` : ''}
                ${i.url ? `<div><span class="text-slate-400 text-sm">URL</span><div class="mt-1 font-mono text-sm text-blue-600 break-all">${esc(i.url)}</div></div>` : ''}
                ${i.metadata && Object.keys(i.metadata).length ? `<div><span class="text-slate-400 text-sm">元数据</span><pre class="mt-1 bg-slate-50 border border-slate-200 rounded-lg p-3 text-sm text-slate-600">${esc(JSON.stringify(i.metadata,null,2))}</pre></div>` : ''}
                <div class="pt-4 border-t border-slate-200">
                    <button onclick="retryIncident('${i.id}')" class="bg-orange-500 hover:bg-orange-600 text-white px-4 py-2 rounded-lg text-sm transition-colors shadow-sm">🔄 重新诊断</button>
                </div>
            </div>`;
    } catch (e) { content.innerHTML = `<div class="text-red-500">${esc(e.message)}</div>`; }
};

window.showTaskDetail = async function(id) {
    if (taskDetailTimer) { clearInterval(taskDetailTimer); taskDetailTimer = null; }
    const modal = document.getElementById('modal-task');
    const content = document.getElementById('modal-task-content');
    modal.classList.remove('hidden');
    content.innerHTML = '<div class="text-slate-400">加载中...</div>';

    async function renderTaskDetail() {
        try {
            const t = await api('/tasks/' + id);
            const isActive = t.status === 'running' || t.status === 'pending' || t.status === 'queued';
            let rptHtml = '';
            if (t.status === 'completed') {
                try {
                    const rpt = await api('/reports/' + id);
                    rptHtml = renderReport(rpt);
                } catch { rptHtml = '<div class="text-slate-400 text-sm mt-4">未找到诊断报告</div>'; }
            }

            let elapsedHtml = '';
            if (isActive && t.started_at) {
                const elapsed = Math.floor((Date.now() - new Date(t.started_at).getTime()) / 1000);
                const min = Math.floor(elapsed / 60);
                const sec = elapsed % 60;
                elapsedHtml = `
                    <div class="col-span-2 bg-blue-50 border border-blue-200 rounded-lg p-3 flex items-center gap-3">
                        <span class="task-pulse inline-block w-3 h-3 rounded-full bg-blue-500"></span>
                        <span class="text-sm text-blue-700">诊断进行中，已运行 <strong id="task-elapsed">${min}分${sec}秒</strong>
                        ${t.num_turns ? ` · 已完成 <strong>${t.num_turns}</strong> 轮对话` : ''}
                        ${t.input_tokens ? ` · 已消耗 <strong>${fmtNum(t.input_tokens + t.output_tokens)}</strong> Token` : ''}
                        </span>
                    </div>`;
            }

            content.innerHTML = `
                <div class="space-y-4">
                    ${elapsedHtml}
                    <div class="grid grid-cols-2 gap-4">
                        <div><span class="text-slate-400 text-sm">任务 ID</span><div class="font-mono text-sm text-slate-700">${esc(t.id)}</div></div>
                        <div><span class="text-slate-400 text-sm">事件 ID</span><div class="font-mono text-sm cursor-pointer text-blue-600 hover:text-blue-700" onclick="closeModal('modal-task');showIncidentDetail('${t.incident_id}')">${esc(t.incident_id)}</div></div>
                        <div><span class="text-slate-400 text-sm">项目</span><div class="text-slate-700">${esc(t.project_key)}</div></div>
                        <div><span class="text-slate-400 text-sm">状态</span><div>${statusBadge(t.status)}</div></div>
                        <div><span class="text-slate-400 text-sm">耗时</span><div class="text-slate-700">${t.duration_ms ? (t.duration_ms/1000).toFixed(1)+' 秒' : '-'}</div></div>
                        <div><span class="text-slate-400 text-sm">对话轮次</span><div class="text-slate-700">${t.num_turns||'-'}</div></div>
                        <div><span class="text-slate-400 text-sm">Token</span><div class="text-slate-700">${t.input_tokens ? fmtNum(t.input_tokens)+' 输入 / '+fmtNum(t.output_tokens)+' 输出' : '-'}</div></div>
                        <div><span class="text-slate-400 text-sm">重试次数</span><div class="text-slate-700">${t.retry_count||0}</div></div>
                        ${t.session_id ? `<div><span class="text-slate-400 text-sm">Amp 会话</span><div class="font-mono text-xs text-blue-600">${esc(t.session_id)}</div></div>` : ''}
                        <div><span class="text-slate-400 text-sm">创建时间</span><div class="text-slate-700">${fmtTime(t.created_at)}</div></div>
                    </div>
                    ${t.error ? `<div><span class="text-slate-400 text-sm">错误信息</span><pre class="mt-1 bg-red-50 border border-red-100 rounded-lg p-3 text-sm text-red-600 whitespace-pre-wrap">${esc(t.error)}</pre></div>` : ''}
                    ${rptHtml}
                </div>`;

            // Auto-refresh while active, stop when finished
            if (isActive && !taskDetailTimer) {
                taskDetailTimer = setInterval(() => {
                    if (modal.classList.contains('hidden')) { clearInterval(taskDetailTimer); taskDetailTimer = null; return; }
                    renderTaskDetail();
                }, 5000);
            } else if (!isActive && taskDetailTimer) {
                clearInterval(taskDetailTimer); taskDetailTimer = null;
                showToast('诊断任务已完成：' + t.status, t.status === 'completed' ? 'success' : 'error');
            }
        } catch (e) { content.innerHTML = `<div class="text-red-500">${esc(e.message)}</div>`; }
    }

    renderTaskDetail();
};

let lastReportHtml = '';

function renderReport(r) {
    if (!r) return '';
    const confBadge = {
        high: '<span class="bg-emerald-50 text-emerald-600 border border-emerald-200 px-2 py-0.5 rounded-full text-xs">高置信度</span>',
        medium: '<span class="bg-amber-50 text-amber-600 border border-amber-200 px-2 py-0.5 rounded-full text-xs">中置信度</span>',
        low: '<span class="bg-red-50 text-red-600 border border-red-200 px-2 py-0.5 rounded-full text-xs">低置信度</span>',
    };
    lastReportHtml = renderMd(r.raw_result || r.summary || '无内容');
    return `
        <div class="pt-4 border-t border-slate-200">
            <div class="flex items-center justify-between mb-3">
                <h4 class="font-bold text-lg text-slate-800">📋 诊断报告</h4>
                <button onclick="openReportFullscreen()" class="text-slate-400 hover:text-blue-600 text-xs px-2 py-1 rounded-lg border border-slate-200 hover:bg-blue-50 transition-colors">⛶ 全屏查看</button>
            </div>
            ${r.tainted ? '<div class="bg-purple-50 border border-purple-200 rounded-lg p-3 text-sm text-purple-700 mb-3">⚠️ <strong>安全告警</strong>：诊断过程中检测到源码被意外修改，已自动回滚。此报告可能不可靠。</div>' : ''}
            <div class="flex items-center gap-3 mb-4">
                ${r.has_issue ? '<span class="text-red-500 font-medium">🔴 发现问题</span>' : '<span class="text-emerald-500 font-medium">🟢 未发现代码问题</span>'}
                ${confBadge[r.confidence]||''}
                ${r.notified ? '<span class="text-blue-600 text-xs">✓ 已通知</span>' : '<span class="text-slate-400 text-xs">○ 未通知</span>'}
            </div>
            ${r.tools_used?.length ? `<div class="mb-3"><span class="text-slate-400 text-xs">使用工具：</span><span class="text-xs text-slate-600">${r.tools_used.map(t=>esc(t)).join(', ')}</span></div>` : ''}
            ${r.skills_used?.length ? `<div class="mb-3"><span class="text-slate-400 text-xs">使用 Skills：</span><span class="text-xs text-slate-600">${r.skills_used.map(s=>esc(s)).join(', ')}</span></div>` : ''}
            <div class="bg-slate-50 border border-slate-200 rounded-lg p-4 report-content text-sm text-slate-600 leading-relaxed max-h-96 overflow-y-auto">
                ${lastReportHtml}
            </div>
        </div>`;
}

window.openReportFullscreen = function() {
    document.getElementById('modal-report-content').innerHTML = lastReportHtml;
    document.getElementById('modal-report').classList.remove('hidden');
};

window.retryIncident = async function(id) {
    const ok = await showConfirm('确定要重新触发诊断吗？');
    if (!ok) return;
    try {
        const r = await api('/incidents/' + id + '/retry', { method: 'POST' });
        showToast('已提交重试，新任务 ID: ' + r.task_id, 'success');
        if (currentPage === 'tasks') loadTasks(taskOffset);
    } catch (e) { showToast('重试失败: ' + e.message, 'error'); }
};

// ── Pagination ──

function renderPagination(cid, count, offset, type) {
    const el = document.getElementById(cid);
    if (!el) return;
    const hasPrev = offset > 0;
    const hasNext = count >= PAGE_SIZE;
    const page = Math.floor(offset / PAGE_SIZE) + 1;
    el.innerHTML = `
        <span>第 ${page} 页</span>
        <div class="flex gap-2">
            <button ${hasPrev?'':'disabled'} class="px-3 py-1 rounded bg-white border border-slate-200 hover:bg-blue-50 hover:border-blue-300 disabled:opacity-40 disabled:cursor-not-allowed transition-colors text-slate-600" data-dir="prev" data-type="${type}">上一页</button>
            <button ${hasNext?'':'disabled'} class="px-3 py-1 rounded bg-white border border-slate-200 hover:bg-blue-50 hover:border-blue-300 disabled:opacity-40 disabled:cursor-not-allowed transition-colors text-slate-600" data-dir="next" data-type="${type}">下一页</button>
        </div>`;
    el.querySelectorAll('button').forEach(btn => {
        btn.addEventListener('click', () => {
            const dir = btn.dataset.dir;
            const newOff = Math.max(0, dir === 'next' ? offset + PAGE_SIZE : offset - PAGE_SIZE);
            if (type === 'incidents') loadIncidents(newOff);
            else loadTasks(newOff);
        });
    });
}

// ── Refresh ──

function startRefresh() {
    stopRefresh();
    refreshTimer = setInterval(() => {
        if (currentPage === 'dashboard') loadDashboard();
        checkHealth();
    }, REFRESH_MS);
}

function stopRefresh() { if (refreshTimer) { clearInterval(refreshTimer); refreshTimer = null; } }

// ── Toast & Confirm ──

function showToast(msg, type = 'info') {
    const container = document.getElementById('toast-container');
    const colors = {
        success: 'bg-emerald-50 border-emerald-200 text-emerald-700',
        error: 'bg-red-50 border-red-200 text-red-700',
        info: 'bg-blue-50 border-blue-200 text-blue-700',
    };
    const icons = { success: '✅', error: '❌', info: 'ℹ️' };
    const el = document.createElement('div');
    el.className = `${colors[type]||colors.info} border rounded-xl px-4 py-3 shadow-md text-sm flex items-center gap-2 animate-slide-in`;
    el.innerHTML = `<span>${icons[type]||icons.info}</span><span>${esc(msg)}</span>`;
    container.appendChild(el);
    setTimeout(() => { el.style.opacity = '0'; el.style.transform = 'translateX(100%)'; setTimeout(() => el.remove(), 300); }, 3500);
}

function showConfirm(msg) {
    return new Promise(resolve => {
        const modal = document.getElementById('modal-confirm');
        document.getElementById('confirm-msg').textContent = msg;
        modal.classList.remove('hidden');
        const cleanup = (result) => { modal.classList.add('hidden'); resolve(result); };
        document.getElementById('confirm-ok').onclick = () => cleanup(true);
        document.getElementById('confirm-cancel').onclick = () => cleanup(false);
    });
}

// ── Helpers ──

function setText(id, v) { const el = document.getElementById(id); if (el) el.textContent = v; }

function sevBadge(s) {
    const m = { critical:'bg-red-50 text-red-600 border border-red-200', warning:'bg-amber-50 text-amber-600 border border-amber-200', info:'bg-blue-50 text-blue-600 border border-blue-200' };
    return `<span class="${m[s]||'bg-slate-100 text-slate-500 border border-slate-200'} px-2 py-0.5 rounded-full text-xs font-medium">${esc(s||'unknown')}</span>`;
}

function statusBadge(s) {
    const m = { completed:'bg-emerald-50 text-emerald-600 border border-emerald-200', running:'bg-amber-50 text-amber-600 border border-amber-200', pending:'bg-blue-50 text-blue-600 border border-blue-200', queued:'bg-blue-50 text-blue-600 border border-blue-200', failed:'bg-red-50 text-red-600 border border-red-200', timeout:'bg-orange-50 text-orange-600 border border-orange-200' };
    return `<span class="${m[s]||'bg-slate-100 text-slate-500 border border-slate-200'} px-2 py-0.5 rounded-full text-xs font-medium">${esc(s||'unknown')}</span>`;
}

function fmtTime(ts) {
    if (!ts) return '-';
    try {
        const d = new Date(ts);
        if (isNaN(d.getTime())) return ts;
        const diff = Date.now() - d.getTime();
        if (diff < 60000) return '刚刚';
        if (diff < 3600000) return Math.floor(diff/60000) + ' 分钟前';
        if (diff < 86400000) return Math.floor(diff/3600000) + ' 小时前';
        return d.toLocaleString('zh-CN', { month:'2-digit', day:'2-digit', hour:'2-digit', minute:'2-digit' });
    } catch { return ts; }
}

function fmtNum(n) {
    if (!n) return '0';
    if (n >= 1e6) return (n/1e6).toFixed(1)+'M';
    if (n >= 1e3) return (n/1e3).toFixed(1)+'K';
    return String(n);
}

function esc(s) {
    if (!s) return '';
    const d = document.createElement('div');
    d.textContent = String(s);
    return d.innerHTML;
}

function renderMd(text) {
    if (!text) return '';
    let h = esc(text);
    h = h.replace(/```(\w*)\n([\s\S]*?)```/g, '<pre><code>$2</code></pre>');
    h = h.replace(/`([^`]+)`/g, '<code>$1</code>');
    h = h.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');
    h = h.replace(/^### (.+)$/gm, '<h3>$1</h3>');
    h = h.replace(/^## (.+)$/gm, '<h2>$1</h2>');
    h = h.replace(/^# (.+)$/gm, '<h1>$1</h1>');
    h = h.replace(/^- (.+)$/gm, '<li>$1</li>');
    h = h.replace(/^(\d+)\. (.+)$/gm, '<li>$2</li>');
    h = h.replace(/((?:<li>.*<\/li>\n?)+)/g, '<ul>$1</ul>');
    h = h.replace(/^&gt; (.+)$/gm, '<blockquote>$1</blockquote>');
    h = h.replace(/\n\n/g, '</p><p>');
    h = h.replace(/\n/g, '<br>');
    return '<p>' + h + '</p>';
}

// ── Init ──
checkAuth();

})();
