const state = {
  status: {},
  shares: [],
  mounts: [],
  shareUsers: [],
  mountUsers: [],
  analytics: { disks: [], shares: [], mounts: [] },
  settings: { auth_enabled: false, global: {} }
};

function esc(str) {
  if (str === null || str === undefined) return '';
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function showToast(msg, type = 'info') {
  const container = document.getElementById('toastContainer');
  const el = document.createElement('div');
  el.className = `toast ${type}`;
  el.textContent = msg;
  container.appendChild(el);
  setTimeout(() => {
    el.style.opacity = '0';
    setTimeout(() => el.remove(), 250);
  }, 3800);
}

async function api(path, options = {}) {
  const opts = {
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
    ...options
  };
  const res = await fetch(path, opts);
  if (res.status === 401) {
    document.getElementById('loginOverlay').classList.add('active');
    throw new Error('Authentication required');
  }
  let data = {};
  try {
    data = await res.json();
  } catch (_) {}
  if (!res.ok) {
    throw new Error(data.error || `Request failed (${res.status})`);
  }
  return data;
}

const VIEW_TITLES = {
  'dashboard': 'Dashboard',
  'shares': 'Samba Shares',
  'share-users': 'Share Users',
  'mounts': 'Samba Mounts',
  'mount-users': 'Mount Users',
  'analytics': 'Disk Analytics',
  'settings': 'Settings'
};

function getViewFromLocation() {
  const slug = window.location.pathname.replace(/^\/+|\/+$/g, '').toLowerCase();
  if (VIEW_TITLES[slug]) {
    return slug;
  }
  const hashSlug = window.location.hash.replace(/^#\/?/, '').toLowerCase();
  if (VIEW_TITLES[hashSlug]) {
    return hashSlug;
  }
  return 'dashboard';
}

function switchView(viewName, historyMode = 'push') {
  const validView = VIEW_TITLES[viewName] ? viewName : 'dashboard';

  document.querySelectorAll('.nav-item').forEach(item => {
    item.classList.toggle('active', item.dataset.view === validView);
  });
  document.querySelectorAll('.view-section').forEach(sec => {
    sec.classList.toggle('active', sec.id === `view-${validView}`);
  });

  document.title = `Samba Manager — ${VIEW_TITLES[validView]}`;

  const targetPath = `/${validView}`;
  if (historyMode === 'push' && window.location.pathname !== targetPath) {
    window.history.pushState({ view: validView }, '', targetPath);
  } else if (historyMode === 'replace' && window.location.pathname !== targetPath) {
    window.history.replaceState({ view: validView }, '', targetPath);
  }

  if (validView === 'analytics') {
    loadAnalytics();
  }
}

function openModal(id) {
  document.getElementById(id).classList.add('open');
}

function closeModal(id) {
  document.getElementById(id).classList.remove('open');
}

// Navigation setup
document.addEventListener('DOMContentLoaded', () => {
  document.querySelectorAll('.nav-item').forEach(item => {
    item.addEventListener('click', () => switchView(item.dataset.view, 'push'));
  });

  window.addEventListener('popstate', () => {
    switchView(getViewFromLocation(), 'none');
  });

  switchView(getViewFromLocation(), 'replace');

  document.getElementById('loginForm').addEventListener('submit', async (e) => {
    e.preventDefault();
    const password = document.getElementById('loginPassword').value;
    try {
      await api('/api/auth/login', {
        method: 'POST',
        body: JSON.stringify({ password })
      });
      document.getElementById('loginPassword').value = '';
      document.getElementById('loginOverlay').classList.remove('active');
      showToast('Unlocked Web UI', 'success');
      refreshAll();
    } catch (err) {
      showToast(err.message, 'error');
    }
  });

  document.getElementById('shareForm').addEventListener('submit', saveShare);
  document.getElementById('mountForm').addEventListener('submit', saveMount);
  document.getElementById('shareUserForm').addEventListener('submit', saveShareUser);
  document.getElementById('mountUserForm').addEventListener('submit', saveMountUser);
  document.getElementById('passwordForm').addEventListener('submit', saveUiPassword);
  document.getElementById('globalSettingsForm').addEventListener('submit', saveGlobalSettings);

  initHelpTooltips();
  refreshAll();
});

function initHelpTooltips() {
  const tipEl = document.getElementById('floatingTooltip');
  if (!tipEl) return;

  document.querySelectorAll('.help-tip').forEach(icon => {
    icon.addEventListener('click', (e) => {
      e.preventDefault();
      e.stopPropagation();
    });
    icon.addEventListener('mouseenter', () => {
      const text = icon.getAttribute('data-tip');
      if (!text) return;
      tipEl.textContent = text;
      tipEl.classList.add('visible');

      const rect = icon.getBoundingClientRect();
      const tipRect = tipEl.getBoundingClientRect();
      let top = rect.top - tipRect.height - 8;
      if (top < 8) {
        top = rect.bottom + 8;
      }
      let left = rect.left + (rect.width / 2) - (tipRect.width / 2);
      left = Math.max(8, Math.min(window.innerWidth - tipRect.width - 8, left));

      tipEl.style.top = `${top}px`;
      tipEl.style.left = `${left}px`;
    });
    icon.addEventListener('mouseleave', () => {
      tipEl.classList.remove('visible');
    });
  });
}

let refreshInFlight = false;
let serviceActionInFlight = false;

async function refreshAll() {
  if (refreshInFlight) return;
  refreshInFlight = true;
  const refreshBtn = document.getElementById('dashRefreshBtn');
  if (refreshBtn) {
    refreshBtn.disabled = true;
    refreshBtn.textContent = 'Refreshing...';
  }
  try {
    const authRes = await fetch('/api/auth/status');
    const authData = await authRes.json();
    if (authData.auth_enabled && !authData.authenticated) {
      document.getElementById('loginOverlay').classList.add('active');
      return;
    }
    document.getElementById('loginOverlay').classList.remove('active');
    document.getElementById('logoutWrap').style.display = authData.auth_enabled ? 'block' : 'none';

    const data = await api('/api/overview');
    state.status = data.status || {};
    state.shares = data.shares || [];
    state.mounts = data.mounts || [];
    state.shareUsers = data.share_users || [];
    state.mountUsers = data.mount_users || [];
    state.analytics = data.analytics || { shares: [], mounts: [] };
    state.settings = data.settings || { auth_enabled: false, global: {} };

    renderSidebarAndDashboard();
    renderShares();
    renderMounts();
    renderShareUsers();
    renderMountUsers();
    renderAnalytics();
    renderSettings();
  } catch (err) {
    if (err.message !== 'Authentication required') {
      showToast(err.message, 'error');
    }
  } finally {
    refreshInFlight = false;
    if (refreshBtn) {
      refreshBtn.disabled = false;
      refreshBtn.textContent = 'Refresh';
    }
  }
}

function updateServiceControlButtons(activeAction = '') {
  const startBtn = document.getElementById('svcBtnStart');
  const restartBtn = document.getElementById('svcBtnRestart');
  const reloadBtn = document.getElementById('svcBtnReload');
  const stopBtn = document.getElementById('svcBtnStop');
  if (!startBtn || !restartBtn || !reloadBtn || !stopBtn) return;

  const smbdActive = state.status.smbd_status === 'active';
  const installed = !!state.status.installed;

  if (serviceActionInFlight || !installed) {
    startBtn.disabled = true;
    restartBtn.disabled = true;
    reloadBtn.disabled = true;
    stopBtn.disabled = true;
    startBtn.textContent = activeAction === 'start' ? 'Starting...' : 'Start';
    restartBtn.textContent = activeAction === 'restart' ? 'Restarting...' : 'Restart';
    reloadBtn.textContent = activeAction === 'reload' ? 'Reloading...' : 'Reload Config';
    stopBtn.textContent = activeAction === 'stop' ? 'Stopping...' : 'Stop';
    return;
  }

  startBtn.disabled = smbdActive;
  startBtn.textContent = smbdActive ? 'Running' : 'Start';

  restartBtn.disabled = !smbdActive;
  restartBtn.textContent = 'Restart';

  reloadBtn.disabled = !smbdActive;
  reloadBtn.textContent = 'Reload Config';

  stopBtn.disabled = !smbdActive;
  stopBtn.textContent = smbdActive ? 'Stop' : 'Stopped';
}

function renderSidebarAndDashboard() {
  const st = state.status;
  const smbdActive = st.smbd_status === 'active';
  const nmbdActive = st.nmbd_status === 'active';

  document.getElementById('sidebarSmbd').innerHTML =
    `<span class="dot ${smbdActive ? 'online' : 'offline'}"></span>${smbdActive ? 'Active' : 'Inactive'}`;
  document.getElementById('sidebarNmbd').innerHTML =
    `<span class="dot ${nmbdActive ? 'online' : 'offline'}"></span>${nmbdActive ? 'Active' : 'Inactive'}`;

  const dashSmbd = document.getElementById('dashSmbdStatus');
  dashSmbd.textContent = smbdActive ? 'Active' : 'Inactive';
  dashSmbd.style.color = smbdActive ? 'var(--success)' : 'var(--danger)';
  document.getElementById('dashSmbVersion').textContent = st.installed ? (st.version || 'Samba installed') : 'Samba not installed';

  updateServiceControlButtons();

  document.getElementById('dashSharesCount').textContent = state.shares.length;
  document.getElementById('dashConnections').textContent = `${st.connections || 0} active client session(s)`;

  const mountedCount = state.mounts.filter(m => m.mounted).length;
  document.getElementById('dashMountsCount').textContent = state.mounts.length;
  document.getElementById('dashMountsActive').textContent = `${mountedCount} currently mounted`;

  document.getElementById('dashUsersCount').textContent = `${state.shareUsers.length} / ${state.mountUsers.length}`;

  // Dashboard Shares Mini-Table
  const sBody = document.getElementById('dashSharesTable');
  if (state.shares.length === 0) {
    sBody.innerHTML = `<tr><td colspan="3" class="empty-state">No Samba shares configured yet</td></tr>`;
  } else {
    sBody.innerHTML = state.shares.slice(0, 6).map(sh => `
      <tr>
        <td><strong>${esc(sh.name)}</strong></td>
        <td><code>${esc(sh.path)}</code></td>
        <td>
          <span class="badge ${sh.read_only ? 'badge-warning' : 'badge-success'}">${sh.read_only ? 'Read-Only' : 'Read/Write'}</span>
          ${sh.guest_ok ? '<span class="badge badge-info">Guest</span>' : ''}
        </td>
      </tr>
    `).join('');
  }

  // Dashboard Mounts Mini-Table
  const mBody = document.getElementById('dashMountsTable');
  if (state.mounts.length === 0) {
    mBody.innerHTML = `<tr><td colspan="3" class="empty-state">No CIFS mounts configured in /etc/fstab</td></tr>`;
  } else {
    mBody.innerHTML = state.mounts.slice(0, 6).map(m => `
      <tr>
        <td><code>${esc(m.remote_path)}</code></td>
        <td><code>${esc(m.mount_point)}</code></td>
        <td>
          <span class="badge ${m.mounted ? 'badge-success' : 'badge-muted'}">${m.mounted ? 'Mounted' : 'Unmounted'}</span>
        </td>
      </tr>
    `).join('');
  }
}

function renderShares() {
  const tbody = document.getElementById('sharesTableBody');
  if (state.shares.length === 0) {
    tbody.innerHTML = `<tr><td colspan="6" class="empty-state">No Samba shares found in <code>/etc/samba/smb.conf</code>. Click "+ Add Samba Share" to create one.</td></tr>`;
    return;
  }

  tbody.innerHTML = state.shares.map(sh => `
    <tr>
      <td>
        <strong>${esc(sh.name)}</strong>
        ${sh.comment ? `<div style="font-size:0.76rem; color:var(--text-muted);">${esc(sh.comment)}</div>` : ''}
      </td>
      <td>
        <code>${esc(sh.path)}</code>
        ${sh.path_exists
          ? '<span class="badge badge-success" style="margin-left:6px;">Exists</span>'
          : '<span class="badge badge-danger" style="margin-left:6px;">Missing</span>'}
      </td>
      <td>
        <span class="badge ${sh.read_only ? 'badge-warning' : 'badge-success'}">
          ${sh.read_only ? 'Read Only' : 'Read / Write'}
        </span>
      </td>
      <td>
        <span class="badge ${sh.guest_ok ? 'badge-info' : 'badge-muted'}">
          ${sh.guest_ok ? 'Guest OK' : 'Auth Required'}
        </span>
      </td>
      <td>
        ${sh.valid_users ? `<div><small style="color:var(--text-muted)">Users:</small> <code>${esc(sh.valid_users)}</code></div>` : '<span style="color:var(--text-muted)">All Auth Users</span>'}
        ${sh.write_list ? `<div style="margin-top:3px;"><small style="color:var(--text-muted)">Write:</small> <code>${esc(sh.write_list)}</code></div>` : ''}
      </td>
      <td>
        <div class="action-group">
          <button class="btn btn-outline btn-sm" onclick="editShare('${esc(sh.name)}')">Edit</button>
          <button class="btn btn-danger btn-sm" onclick="deleteShare('${esc(sh.name)}')">Delete</button>
        </div>
      </td>
    </tr>
  `).join('');
}

function renderMounts() {
  const tbody = document.getElementById('mountsTableBody');
  if (state.mounts.length === 0) {
    tbody.innerHTML = `<tr><td colspan="7" class="empty-state">No CIFS mounts configured in <code>/etc/fstab</code>. Click "+ Add Samba Mount" to add one.</td></tr>`;
    return;
  }

  tbody.innerHTML = state.mounts.map(m => `
    <tr>
      <td>
        <span class="badge ${m.mounted ? 'badge-success' : 'badge-muted'}">
          ${m.mounted ? '● Mounted' : '○ Unmounted'}
        </span>
      </td>
      <td><code>${esc(m.remote_path)}</code></td>
      <td><code>${esc(m.mount_point)}</code></td>
      <td>
        ${m.mount_user
          ? `<span class="badge badge-info">${esc(m.mount_user)}</span>`
          : '<span class="badge badge-muted">Guest</span>'}
      </td>
      <td>
        <span class="badge ${m.read_only ? 'badge-warning' : 'badge-success'}">${m.read_only ? 'RO' : 'RW'}</span>
        ${m.smb_version ? `<span class="badge badge-muted">v${esc(m.smb_version)}</span>` : ''}
      </td>
      <td>
        <span class="badge ${m.in_fstab ? 'badge-success' : 'badge-warning'}">
          ${m.in_fstab ? '/etc/fstab' : 'Runtime Only'}
        </span>
      </td>
      <td>
        <div class="action-group">
          ${m.mounted
            ? `<button class="btn btn-outline btn-sm" onclick="toggleMount('${esc(m.mount_point)}', 'unmount')">Unmount</button>`
            : `<button class="btn btn-success btn-sm" onclick="toggleMount('${esc(m.mount_point)}', 'mount')">Mount</button>`}
          <button class="btn btn-outline btn-sm" onclick="editMount('${esc(m.mount_point)}')">Edit</button>
          <button class="btn btn-danger btn-sm" onclick="deleteMount('${esc(m.mount_point)}')">Delete</button>
        </div>
      </td>
    </tr>
  `).join('');
}

function renderShareUsers() {
  const tbody = document.getElementById('shareUsersTableBody');
  if (state.shareUsers.length === 0) {
    tbody.innerHTML = `<tr><td colspan="5" class="empty-state">No Samba share users found in <code>pdbedit</code>. Click "+ Add Share User" to create one.</td></tr>`;
    return;
  }

  tbody.innerHTML = state.shareUsers.map(u => `
    <tr>
      <td><strong>${esc(u.username)}</strong></td>
      <td><code>${u.uid}</code></td>
      <td>
        <span class="badge ${u.enabled ? 'badge-success' : 'badge-danger'}">
          ${u.enabled ? 'Enabled' : 'Disabled'}
        </span>
      </td>
      <td>
        ${(u.shares && u.shares.length > 0)
          ? u.shares.map(s => `<span class="badge badge-info" style="margin-right:4px;">${esc(s)}</span>`).join('')
          : '<span style="color:var(--text-muted)">None</span>'}
      </td>
      <td>
        <div class="action-group">
          <button class="btn btn-outline btn-sm" onclick="openShareUserModal('${esc(u.username)}')">Change Password</button>
          <button class="btn btn-outline btn-sm" onclick="toggleShareUser('${esc(u.username)}', ${!u.enabled})">
            ${u.enabled ? 'Disable' : 'Enable'}
          </button>
          <button class="btn btn-danger btn-sm" onclick="deleteShareUser('${esc(u.username)}')">Delete</button>
        </div>
      </td>
    </tr>
  `).join('');
}

function renderMountUsers() {
  const tbody = document.getElementById('mountUsersTableBody');
  if (state.mountUsers.length === 0) {
    tbody.innerHTML = `<tr><td colspan="5" class="empty-state">No CIFS credential files in <code>/etc/samba/credentials/</code>. Click "+ Add Mount User" to create one.</td></tr>`;
    return;
  }

  tbody.innerHTML = state.mountUsers.map(mu => `
    <tr>
      <td><strong>${esc(mu.username)}</strong></td>
      <td>${mu.domain ? `<code>${esc(mu.domain)}</code>` : '<span style="color:var(--text-muted)">Default</span>'}</td>
      <td><code>${esc(mu.cred_path)}</code></td>
      <td>
        ${(mu.used_by && mu.used_by.length > 0)
          ? mu.used_by.map(mp => `<span class="badge badge-info" style="margin-right:4px;">${esc(mp)}</span>`).join('')
          : '<span style="color:var(--text-muted)">Unused</span>'}
      </td>
      <td>
        <div class="action-group">
          <button class="btn btn-outline btn-sm" onclick="editMountUser('${esc(mu.name)}')">Edit</button>
          <button class="btn btn-danger btn-sm" onclick="deleteMountUser('${esc(mu.name)}')">Delete</button>
        </div>
      </td>
    </tr>
  `).join('');
}

function progressClass(pct) {
  if (pct >= 90) return 'progress-crit';
  if (pct >= 75) return 'progress-warn';
  return 'progress-ok';
}

function renderShareStorageCard(sh) {
  const modeBadge = sh.read_only
    ? `<span class="badge badge-warning" style="margin-left:6px;">Read Only</span>`
    : `<span class="badge badge-info" style="margin-left:6px;">Read / Write</span>`;
  const guestBadge = sh.guest_ok
    ? `<span class="badge badge-success" style="margin-left:4px;">Guest Access</span>`
    : `<span class="badge badge-muted" style="margin-left:4px;">${sh.valid_users ? esc(sh.valid_users) : 'Auth Users'}</span>`;

  if (!sh.online) {
    return `
      <div style="padding:14px; border:1px solid var(--border-subtle); border-radius:var(--radius-sm); margin-bottom:12px; background:var(--bg-input);">
        <div style="display:flex; justify-content:space-between; align-items:center;">
          <div>
            <strong>${esc(sh.name)}</strong>
            ${modeBadge}
            ${guestBadge}
          </div>
          <span class="badge badge-danger">Path Missing</span>
        </div>
        <div style="font-size:0.78rem; color:var(--text-muted); margin-top:4px;">Directory: <code>${esc(sh.path)}</code></div>
      </div>
    `;
  }

  const pct = sh.use_percent || 0;
  return `
    <div style="padding:14px; border:1px solid var(--border-subtle); border-radius:var(--radius-sm); margin-bottom:12px; background:var(--bg-input);">
      <div style="display:flex; justify-content:space-between; align-items:center;">
        <div>
          <strong>${esc(sh.name)}</strong>
          ${modeBadge}
          ${guestBadge}
        </div>
        <span style="font-weight:700; font-size:0.9rem;">${pct}%</span>
      </div>
      <div style="font-size:0.76rem; color:var(--text-muted); margin-top:4px;">
        Directory: <code>${esc(sh.path)}</code>${sh.comment ? ` • ${esc(sh.comment)}` : ''}
      </div>
      <div class="progress-track">
        <div class="progress-fill ${progressClass(pct)}" style="width:${Math.min(100, Math.max(2, pct))}%"></div>
      </div>
      <div style="display:flex; justify-content:space-between; font-size:0.78rem; color:var(--text-secondary);">
        <span>Used: <strong>${esc(sh.used_human)}</strong></span>
        <span>Free: <strong>${esc(sh.available_human)}</strong></span>
        <span>Total: <strong>${esc(sh.total_human)}</strong></span>
      </div>
    </div>
  `;
}

function renderMountStorageCard(m) {
  const modeBadge = m.read_only
    ? `<span class="badge badge-warning" style="margin-left:6px;">RO</span>`
    : `<span class="badge badge-info" style="margin-left:6px;">RW</span>`;
  const userBadge = m.mount_user
    ? `<span class="badge badge-muted" style="margin-left:4px;">User: ${esc(m.mount_user)}</span>`
    : `<span class="badge badge-muted" style="margin-left:4px;">Guest</span>`;

  if (!m.online) {
    return `
      <div style="padding:14px; border:1px solid var(--border-subtle); border-radius:var(--radius-sm); margin-bottom:12px; background:var(--bg-input);">
        <div style="display:flex; justify-content:space-between; align-items:center;">
          <div>
            <strong><code>${esc(m.remote_path)}</code></strong>
            ${modeBadge}
            ${userBadge}
          </div>
          <span class="badge badge-muted">Unmounted</span>
        </div>
        <div style="font-size:0.78rem; color:var(--text-muted); margin-top:4px;">Mount Point: <code>${esc(m.mount_point)}</code></div>
      </div>
    `;
  }

  const pct = m.use_percent || 0;
  return `
    <div style="padding:14px; border:1px solid var(--border-subtle); border-radius:var(--radius-sm); margin-bottom:12px; background:var(--bg-input);">
      <div style="display:flex; justify-content:space-between; align-items:center;">
        <div>
          <strong><code>${esc(m.remote_path)}</code></strong>
          ${modeBadge}
          ${userBadge}
        </div>
        <span style="font-weight:700; font-size:0.9rem;">${pct}%</span>
      </div>
      <div style="font-size:0.76rem; color:var(--text-muted); margin-top:4px;">
        Mount Point: <code>${esc(m.mount_point)}</code>${m.smb_version ? ` • SMB ${esc(m.smb_version)}` : ''}
      </div>
      <div class="progress-track">
        <div class="progress-fill ${progressClass(pct)}" style="width:${Math.min(100, Math.max(2, pct))}%"></div>
      </div>
      <div style="display:flex; justify-content:space-between; font-size:0.78rem; color:var(--text-secondary);">
        <span>Used: <strong>${esc(m.used_human)}</strong></span>
        <span>Free: <strong>${esc(m.available_human)}</strong></span>
        <span>Total: <strong>${esc(m.total_human)}</strong></span>
      </div>
    </div>
  `;
}

function renderAnalytics() {
  const { shares = [], mounts = [] } = state.analytics;

  const onlineShares = shares.filter(s => s.online).length;
  const sharesSummaryEl = document.getElementById('analyticsSharesSummary');
  if (sharesSummaryEl) {
    sharesSummaryEl.textContent = `${onlineShares} / ${shares.length}`;
  }

  const onlineMounts = mounts.filter(m => m.online).length;
  const mountsSummaryEl = document.getElementById('analyticsMountsSummary');
  if (mountsSummaryEl) {
    mountsSummaryEl.textContent = `${onlineMounts} / ${mounts.length}`;
  }

  const sContainer = document.getElementById('analyticsSharesContainer');
  if (shares.length === 0) {
    sContainer.innerHTML = `<div class="empty-state">No Samba shares configured</div>`;
  } else {
    sContainer.innerHTML = shares.map(s => renderShareStorageCard(s)).join('');
  }

  const mContainer = document.getElementById('analyticsMountsContainer');
  if (mounts.length === 0) {
    mContainer.innerHTML = `<div class="empty-state">No Samba mounts configured</div>`;
  } else {
    mContainer.innerHTML = mounts.map(m => renderMountStorageCard(m)).join('');
  }
}

async function loadAnalytics() {
  try {
    state.analytics = await api('/api/analytics');
    renderAnalytics();
  } catch (err) {
    showToast(err.message, 'error');
  }
}

function renderSettings() {
  const badge = document.getElementById('authStatusBadge');
  if (state.settings.auth_enabled) {
    badge.className = 'badge badge-success';
    badge.textContent = 'Password Protected';
  } else {
    badge.className = 'badge badge-muted';
    badge.textContent = 'No Password (Default)';
  }

  const verBadge = document.getElementById('updateStatusBadge') || document.getElementById('currentVersionBadge');
  if (verBadge && state.settings.version) {
    verBadge.className = 'badge badge-info';
    verBadge.textContent = state.settings.version;
  }

  const g = state.settings.global || {};
  document.getElementById('gsWorkgroup').value = g.workgroup || 'WORKGROUP';
  document.getElementById('gsServerString').value = g.server_string || 'Samba Server (%h)';
  document.getElementById('gsHostsAllow').value = g.hosts_allow || '';
  document.getElementById('gsHostsDeny').value = g.hosts_deny || '';
}

// Service Control
async function controlService(action) {
  if (serviceActionInFlight) return;
  const smbdActive = state.status.smbd_status === 'active';
  if (action === 'start' && smbdActive) return;
  if (action !== 'start' && !smbdActive) return;

  serviceActionInFlight = true;
  updateServiceControlButtons(action);

  try {
    await api('/api/service', {
      method: 'POST',
      body: JSON.stringify({ action })
    });
    showToast(`Samba service ${action} completed`, 'success');
    await refreshAll();
  } catch (err) {
    showToast(err.message, 'error');
  } finally {
    serviceActionInFlight = false;
    updateServiceControlButtons();
  }
}

// Share Modal & CRUD
function populateShareUserDropdowns(validSelected = '', writeSelected = '') {
  const validSel = document.getElementById('shareValidUsers');
  const writeSel = document.getElementById('shareWriteList');

  const userNames = state.shareUsers.map(u => u.username);

  let validOpts = `<option value="">-- All Authenticated Users --</option>`;
  validOpts += `<option value="@smbusers" ${validSelected === '@smbusers' ? 'selected' : ''}>All Share Users (@smbusers)</option>`;
  userNames.forEach(uname => {
    validOpts += `<option value="${esc(uname)}" ${validSelected === uname ? 'selected' : ''}>${esc(uname)}</option>`;
  });
  if (validSelected && validSelected !== '@smbusers' && !userNames.includes(validSelected)) {
    validOpts += `<option value="${esc(validSelected)}" selected>${esc(validSelected)}</option>`;
  }
  validSel.innerHTML = validOpts;

  let writeOpts = `<option value="">-- None (Use Share Mode) --</option>`;
  writeOpts += `<option value="@smbusers" ${writeSelected === '@smbusers' ? 'selected' : ''}>All Share Users (@smbusers)</option>`;
  userNames.forEach(uname => {
    writeOpts += `<option value="${esc(uname)}" ${writeSelected === uname ? 'selected' : ''}>${esc(uname)}</option>`;
  });
  if (writeSelected && writeSelected !== '@smbusers' && !userNames.includes(writeSelected)) {
    writeOpts += `<option value="${esc(writeSelected)}" selected>${esc(writeSelected)}</option>`;
  }
  writeSel.innerHTML = writeOpts;
}

function openShareModal() {
  document.getElementById('shareModalTitle').textContent = 'Add Samba Share';
  document.getElementById('shareOriginalName').value = '';
  document.getElementById('shareName').value = '';
  document.getElementById('sharePath').value = '/srv/samba/';
  document.getElementById('shareComment').value = '';
  document.getElementById('shareReadOnly').checked = false;
  document.getElementById('shareGuestOK').checked = false;
  populateShareUserDropdowns('', '');
  document.getElementById('shareCreateMask').value = '0744';
  document.getElementById('shareDirectoryMask').value = '0755';
  openModal('shareModal');
}

function editShare(name) {
  const sh = state.shares.find(s => s.name === name);
  if (!sh) return;
  document.getElementById('shareModalTitle').textContent = `Edit Share: ${sh.name}`;
  document.getElementById('shareOriginalName').value = sh.name;
  document.getElementById('shareName').value = sh.name;
  document.getElementById('sharePath').value = sh.path;
  document.getElementById('shareComment').value = sh.comment || '';
  document.getElementById('shareReadOnly').checked = !!sh.read_only;
  document.getElementById('shareGuestOK').checked = !!sh.guest_ok;
  populateShareUserDropdowns(sh.valid_users || '', sh.write_list || '');
  document.getElementById('shareCreateMask').value = sh.create_mask || '0744';
  document.getElementById('shareDirectoryMask').value = sh.directory_mask || '0755';
  openModal('shareModal');
}

async function saveShare(e) {
  e.preventDefault();
  const payload = {
    original_name: document.getElementById('shareOriginalName').value.trim(),
    create_dir: true,
    share: {
      name: document.getElementById('shareName').value.trim(),
      path: document.getElementById('sharePath').value.trim(),
      comment: document.getElementById('shareComment').value.trim(),
      browseable: true,
      read_only: document.getElementById('shareReadOnly').checked,
      guest_ok: document.getElementById('shareGuestOK').checked,
      valid_users: document.getElementById('shareValidUsers').value.trim(),
      write_list: document.getElementById('shareWriteList').value.trim(),
      create_mask: document.getElementById('shareCreateMask').value.trim(),
      directory_mask: document.getElementById('shareDirectoryMask').value.trim(),
      force_group: 'smbusers'
    }
  };
  try {
    await api('/api/shares', { method: 'POST', body: JSON.stringify(payload) });
    closeModal('shareModal');
    showToast('Samba share saved to /etc/samba/smb.conf', 'success');
    refreshAll();
  } catch (err) {
    showToast(err.message, 'error');
  }
}

async function deleteShare(name) {
  if (!confirm(`Remove Samba share "${name}" from /etc/samba/smb.conf? (Files on disk will not be deleted)`)) return;
  try {
    await api(`/api/shares?name=${encodeURIComponent(name)}`, { method: 'DELETE' });
    showToast(`Share "${name}" removed`, 'success');
    refreshAll();
  } catch (err) {
    showToast(err.message, 'error');
  }
}

// Mount Modal & CRUD
function populateMountUserSelect(selected = '') {
  const sel = document.getElementById('mountUserSelect');
  sel.innerHTML = `<option value="">-- Guest Access (No Credentials) --</option>` +
    state.mountUsers.map(mu =>
      `<option value="${esc(mu.name)}" ${mu.name === selected ? 'selected' : ''}>${esc(mu.name)} (${esc(mu.username)})</option>`
    ).join('');
}

function openMountModal() {
  document.getElementById('mountModalTitle').textContent = 'Add Samba Mount (/etc/fstab)';
  document.getElementById('mountOriginalPoint').value = '';
  document.getElementById('mountRemotePath').value = '';
  document.getElementById('mountLocalPoint').value = '/mnt/';
  populateMountUserSelect('');
  document.getElementById('mountSmbVersion').value = '';
  document.getElementById('mountUID').value = '';
  document.getElementById('mountGID').value = '';
  document.getElementById('mountExtraOptions').value = '';
  document.getElementById('mountReadOnly').checked = false;
  openModal('mountModal');
}

function editMount(mountPoint) {
  const m = state.mounts.find(item => item.mount_point === mountPoint);
  if (!m) return;
  document.getElementById('mountModalTitle').textContent = `Edit Mount: ${m.mount_point}`;
  document.getElementById('mountOriginalPoint').value = m.mount_point;
  document.getElementById('mountRemotePath').value = m.remote_path;
  document.getElementById('mountLocalPoint').value = m.mount_point;
  populateMountUserSelect(m.mount_user || '');
  document.getElementById('mountSmbVersion').value = m.smb_version || '';
  document.getElementById('mountUID').value = m.uid || '';
  document.getElementById('mountGID').value = m.gid || '';
  document.getElementById('mountExtraOptions').value = m.extra_options || '';
  document.getElementById('mountReadOnly').checked = !!m.read_only;
  openModal('mountModal');
}

async function saveMount(e) {
  e.preventDefault();
  const mountUser = document.getElementById('mountUserSelect').value;
  const payload = {
    original_mount_point: document.getElementById('mountOriginalPoint').value.trim(),
    mount_now: true,
    mount: {
      remote_path: document.getElementById('mountRemotePath').value.trim(),
      mount_point: document.getElementById('mountLocalPoint').value.trim(),
      mount_user: mountUser,
      guest: mountUser === '',
      read_only: document.getElementById('mountReadOnly').checked,
      smb_version: document.getElementById('mountSmbVersion').value,
      uid: document.getElementById('mountUID').value.trim(),
      gid: document.getElementById('mountGID').value.trim(),
      extra_options: document.getElementById('mountExtraOptions').value.trim()
    }
  };
  try {
    const res = await api('/api/mounts', { method: 'POST', body: JSON.stringify(payload) });
    closeModal('mountModal');
    showToast(res.message || 'Mount saved to /etc/fstab', 'success');
    refreshAll();
  } catch (err) {
    showToast(err.message, 'error');
  }
}

async function toggleMount(mountPoint, action) {
  try {
    await api('/api/mounts/action', {
      method: 'POST',
      body: JSON.stringify({ mount_point: mountPoint, action })
    });
    showToast(`${action === 'mount' ? 'Mounted' : 'Unmounted'} ${mountPoint}`, 'success');
    refreshAll();
  } catch (err) {
    showToast(err.message, 'error');
  }
}

async function deleteMount(mountPoint) {
  if (!confirm(`Unmount and remove "${mountPoint}" from /etc/fstab?`)) return;
  try {
    await api(`/api/mounts?mount_point=${encodeURIComponent(mountPoint)}`, { method: 'DELETE' });
    showToast(`Mount ${mountPoint} removed from /etc/fstab`, 'success');
    refreshAll();
  } catch (err) {
    showToast(err.message, 'error');
  }
}

// Share Users Modal & CRUD
function openShareUserModal(existingUsername = '') {
  const uInput = document.getElementById('suUsername');
  document.getElementById('suPassword').value = '';
  if (existingUsername) {
    document.getElementById('shareUserModalTitle').textContent = `Change Password: ${existingUsername}`;
    uInput.value = existingUsername;
    uInput.readOnly = true;
  } else {
    document.getElementById('shareUserModalTitle').textContent = 'Add Samba Share User';
    uInput.value = '';
    uInput.readOnly = false;
  }
  openModal('shareUserModal');
}

async function saveShareUser(e) {
  e.preventDefault();
  const username = document.getElementById('suUsername').value.trim();
  const password = document.getElementById('suPassword').value;
  try {
    await api('/api/share-users', {
      method: 'POST',
      body: JSON.stringify({ username, password })
    });
    closeModal('shareUserModal');
    showToast(`Samba share user "${username}" saved`, 'success');
    refreshAll();
  } catch (err) {
    showToast(err.message, 'error');
  }
}

async function toggleShareUser(username, enabled) {
  try {
    await api('/api/share-users/status', {
      method: 'POST',
      body: JSON.stringify({ username, enabled })
    });
    showToast(`User "${username}" ${enabled ? 'enabled' : 'disabled'}`, 'success');
    refreshAll();
  } catch (err) {
    showToast(err.message, 'error');
  }
}

async function deleteShareUser(username) {
  if (!confirm(`Remove Samba share user "${username}" from smbpasswd database?`)) return;
  try {
    await api(`/api/share-users?username=${encodeURIComponent(username)}`, { method: 'DELETE' });
    showToast(`Samba share user "${username}" removed`, 'success');
    refreshAll();
  } catch (err) {
    showToast(err.message, 'error');
  }
}

// Mount Users Modal & CRUD
function openMountUserModal() {
  document.getElementById('mountUserModalTitle').textContent = 'Add Samba Mount User Credential';
  const pInput = document.getElementById('muProfileName');
  pInput.value = '';
  pInput.readOnly = false;
  document.getElementById('muUsername').value = '';
  const pwInput = document.getElementById('muPassword');
  pwInput.value = '';
  pwInput.required = true;
  pwInput.placeholder = 'Enter remote SMB password';
  document.getElementById('muDomain').value = '';
  openModal('mountUserModal');
}

function editMountUser(profileName) {
  const mu = state.mountUsers.find(item => item.name === profileName);
  if (!mu) return;
  document.getElementById('mountUserModalTitle').textContent = `Edit Credential: ${mu.name}`;
  const pInput = document.getElementById('muProfileName');
  pInput.value = mu.name;
  pInput.readOnly = true;
  document.getElementById('muUsername').value = mu.username || '';
  const pwInput = document.getElementById('muPassword');
  pwInput.value = '';
  pwInput.required = false;
  pwInput.placeholder = 'Leave blank to keep existing password';
  document.getElementById('muDomain').value = mu.domain || '';
  openModal('mountUserModal');
}

async function saveMountUser(e) {
  e.preventDefault();
  const payload = {
    name: document.getElementById('muProfileName').value.trim(),
    username: document.getElementById('muUsername').value.trim(),
    password: document.getElementById('muPassword').value,
    domain: document.getElementById('muDomain').value.trim()
  };
  try {
    const res = await api('/api/mount-users', {
      method: 'POST',
      body: JSON.stringify(payload)
    });
    closeModal('mountUserModal');
    showToast(`Saved to /etc/samba/credentials/${res.name || payload.username}.cred`, 'success');
    refreshAll();
  } catch (err) {
    showToast(err.message, 'error');
  }
}

async function deleteMountUser(name) {
  if (!confirm(`Delete CIFS credential file /etc/samba/credentials/${name}.cred?`)) return;
  try {
    await api(`/api/mount-users?name=${encodeURIComponent(name)}`, { method: 'DELETE' });
    showToast(`Credential profile "${name}" deleted`, 'success');
    refreshAll();
  } catch (err) {
    showToast(err.message, 'error');
  }
}

// Settings Handlers
async function saveUiPassword(e) {
  e.preventDefault();
  const password = document.getElementById('newUiPassword').value;
  try {
    await api('/api/settings/password', {
      method: 'POST',
      body: JSON.stringify({ password })
    });
    document.getElementById('newUiPassword').value = '';
    showToast(password ? 'Web UI password enabled' : 'Web UI password disabled (No Auth)', 'success');
    refreshAll();
  } catch (err) {
    showToast(err.message, 'error');
  }
}

async function disableUiPassword() {
  try {
    await api('/api/settings/password', {
      method: 'POST',
      body: JSON.stringify({ password: '' })
    });
    document.getElementById('newUiPassword').value = '';
    showToast('Web UI authentication disabled (No Auth)', 'success');
    refreshAll();
  } catch (err) {
    showToast(err.message, 'error');
  }
}

async function saveGlobalSettings(e) {
  e.preventDefault();
  const payload = {
    workgroup: document.getElementById('gsWorkgroup').value.trim(),
    server_string: document.getElementById('gsServerString').value.trim(),
    security: 'user',
    map_to_guest: 'Bad User',
    guest_account: 'nobody',
    log_level: '1',
    hosts_allow: document.getElementById('gsHostsAllow').value.trim(),
    hosts_deny: document.getElementById('gsHostsDeny').value.trim(),
    usershare_allow_guests: 'yes'
  };
  try {
    await api('/api/settings/global', {
      method: 'POST',
      body: JSON.stringify(payload)
    });
    showToast('Global Samba settings saved & reloaded', 'success');
    refreshAll();
  } catch (err) {
    showToast(err.message, 'error');
  }
}

async function logout() {
  try {
    await api('/api/auth/logout', { method: 'POST' });
    refreshAll();
  } catch (err) {
    showToast(err.message, 'error');
  }
}

async function checkForUpdates() {
  const btn = document.getElementById('checkUpdateBtn');
  const applyBtn = document.getElementById('applyUpdateBtn');
  const statusEl = document.getElementById('updateStatusText');
  const badgeEl = document.getElementById('updateStatusBadge') || document.getElementById('currentVersionBadge');
  if (!btn || !statusEl) return;

  const currentVer = state.settings.version || (badgeEl ? badgeEl.textContent : '');

  btn.disabled = true;
  btn.textContent = 'Checking...';
  statusEl.style.color = 'var(--text-secondary)';
  statusEl.textContent = 'Checking TankObliterator/smb-gui commits...';

  try {
    const res = await api('/api/settings/update/check', { method: 'POST' });
    statusEl.style.color = 'var(--text-secondary)';
    statusEl.textContent = res.message;
    if (res.update_available) {
      if (badgeEl) {
        badgeEl.className = 'badge badge-warning';
        badgeEl.textContent = `${res.current_version} → ${res.latest_version}`;
      }
      applyBtn.style.display = 'inline-flex';
      showToast(res.message || `New update ${res.latest_version} available!`, 'info');
    } else {
      if (badgeEl) {
        badgeEl.className = 'badge badge-success';
        badgeEl.textContent = res.current_version || currentVer;
      }
      applyBtn.style.display = 'none';
      showToast(res.message || 'Up to date', 'success');
    }
  } catch (err) {
    if (badgeEl) {
      badgeEl.className = 'badge badge-danger';
      badgeEl.textContent = currentVer;
    }
    applyBtn.style.display = 'none';
    statusEl.style.color = '#f87171';
    statusEl.textContent = err.message;
    showToast(err.message, 'error');
  } finally {
    btn.disabled = false;
    btn.textContent = 'Check for Updates';
  }
}

async function applyUpdate() {
  const applyBtn = document.getElementById('applyUpdateBtn');
  const statusEl = document.getElementById('updateStatusText');
  if (!confirm('Download and install the latest version from TankObliterator/smb-gui and restart smb-gui?')) return;

  applyBtn.disabled = true;
  applyBtn.textContent = 'Updating...';
  statusEl.textContent = 'Downloading and installing update...';

  try {
    const res = await api('/api/settings/update/apply', { method: 'POST' });
    showToast(res.message || 'Update installed, restarting service...', 'success');
    statusEl.textContent = 'Restarting smb-gui service...';
    setTimeout(() => {
      window.location.reload();
    }, 2500);
  } catch (err) {
    statusEl.textContent = `Update failed: ${err.message}`;
    showToast(err.message, 'error');
    applyBtn.disabled = false;
    applyBtn.textContent = 'Install Update & Restart';
  }
}
