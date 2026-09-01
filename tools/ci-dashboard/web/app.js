const state = { snapshot: null, cards: new Map(), previous: new Map(), filters: { search: "", repo: "", trigger: "" } };
const lanes = { queued: document.querySelector("#queued"), running: document.querySelector("#running"), completed: document.querySelector("#completed") };
const template = document.querySelector("#card-template");
const groupTemplate = document.querySelector("#group-template");
const reducedMotion = matchMedia("(prefers-reduced-motion: reduce)").matches;

const escapeHTML = value => String(value ?? "").replace(/[&<>'"]/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;","'":"&#39;",'"':"&quot;"}[c]));
const date = value => value ? new Date(value) : null;
const startTime = card => date(card.runStartedAt) || date(card.createdAt);
const duration = ms => {
  ms = Math.max(0, ms || 0);
  const seconds = Math.floor(ms / 1000), h = Math.floor(seconds / 3600), m = Math.floor(seconds % 3600 / 60), s = seconds % 60;
  return h ? `${h}h ${String(m).padStart(2,"0")}m ${String(s).padStart(2,"0")}s` : `${m}m ${String(s).padStart(2,"0")}s`;
};
const age = value => value ? duration(Date.now() - date(value).getTime()) + " ago" : "—";
const localTime = value => value ? date(value).toLocaleTimeString([], {hour:"2-digit", minute:"2-digit"}) : "—";
const failed = value => ["failure", "timed_out", "action_required", "startup_failure"].includes(value);
const statusText = card => failed(card.conclusion) ? `✕ ${card.conclusion.replaceAll("_", " ")}` : card.conclusion === "success" ? "✓ success" : card.status === "in_progress" ? "● running" : card.status;
const statusClass = card => failed(card.conclusion) ? "failure" : card.conclusion === "success" ? "success" : ["cancelled","skipped","neutral"].includes(card.conclusion) ? "cancelled" : card.status === "in_progress" ? "running" : "queued";
const heat = ms => ms < 300000 ? "#38c976" : ms < 900000 ? "#a6d957" : ms < 1800000 ? "#ffb547" : ms < 3600000 ? "#ff7849" : "#ff4d67";

function cardElapsed(card) {
  const start = startTime(card);
  if (!start) return 0;
  const end = card.status === "completed" ? date(card.updatedAt) || new Date() : new Date();
  return Math.max(0, end - start);
}

function cardMatches(card) {
  const haystack = [card.repo, card.workflow, card.topic, card.headBranch, card.actor?.login, card.pr?.number, card.pr?.title].join(" ").toLowerCase();
  return (!state.filters.search || haystack.includes(state.filters.search)) && (!state.filters.repo || card.repo === state.filters.repo) && (!state.filters.trigger || card.event === state.filters.trigger);
}

function makeCard(card) {
  const node = template.content.firstElementChild.cloneNode(true);
  node.dataset.key = card.key;
  node.addEventListener("click", () => openDrawer(node.dataset.key));
  node.addEventListener("keydown", event => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); openDrawer(node.dataset.key); } });
  return node;
}

function updateCard(node, card, old) {
  node.dataset.key = card.key;
  node.className = `run-card ${statusClass(card)}${card.isSubmodule ? " submodule" : ""}${card.repoStale ? " stale" : ""}`;
  if (old && card.attempt > old.attempt && old.status === "completed" && card.status !== "completed") {
    node.classList.add("reborn");
    setTimeout(() => node.classList.remove("reborn"), 950);
  }
  node.querySelector(".status-badge").textContent = statusText(card);
  node.querySelector(".workflow").textContent = card.jobName || `Workflow run #${card.runNumber}`;
  node.querySelector(".topic").textContent = card.kind === "job" ? jobContext(card) : card.topic;
  node.querySelector(".schedule").textContent = scheduleInfo(card);
  node.querySelector(".actor").textContent = card.actor?.login ? `@${card.actor.login}` : "unknown actor";
  node.querySelector(".start-time").textContent = localTime(card.runStartedAt || card.createdAt);
  node.querySelector(".jobs").textContent = card.kind === "job" ? `JOB · run #${card.runNumber}` : `RUN #${card.runNumber}`;
  node.querySelector(".attempt-row").innerHTML = card.attempt > 1 ? `<span class="attempt-pill">↻ Attempt ${card.attempt} · previous attempt retained</span>` : "";
  updateElapsed(node, card);
}

function jobContext(card) {
  if (card.status === "queued") return "Scheduled · waiting for runner capacity";
  if (card.status === "in_progress") return "Dispatched · runner is executing";
  return card.conclusion ? `Finished · ${card.conclusion.replaceAll("_", " ")}` : "Finished";
}

function scheduleInfo(card) {
  if (card.kind !== "job") return card.headBranch || (card.headSha || "").slice(0, 7) || "—";
  const labels = (card.runnerLabels || []).join(" · ");
  if (card.runnerName) return `runner: ${card.runnerName}${labels ? ` · ${labels}` : ""}`;
  return labels ? `target: ${labels}` : "target runner: not reported by GitHub";
}

function makeGroup(card) {
  const group = groupTemplate.content.firstElementChild.cloneNode(true);
  group.dataset.runKey = `${card.owner}/${card.repo}/${card.runId}`;
  group.querySelector(".group-repo").textContent = card.repo;
  group.querySelector(".submodule-badge").style.display = card.isSubmodule ? "inline" : "none";
  group.querySelector(".group-workflow").textContent = card.workflow;
  group.querySelector(".group-attempt").textContent = card.attempt > 1 ? `ATTEMPT ${card.attempt}` : `RUN #${card.runNumber}`;
  group.querySelector(".group-source").textContent = card.pr ? `PR #${card.pr.number} · ${card.pr.title || card.topic}` : `${card.event === "workflow_dispatch" ? "MANUAL" : card.event} · ${card.topic}`;
  group.querySelector(".group-branch").textContent = card.headBranch || (card.headSha || "").slice(0, 7) || "—";
  return group;
}

function updateElapsed(node, card) {
  const ms = cardElapsed(card), color = heat(ms);
  node.style.setProperty("--heat", failed(card.conclusion) ? "#ff4d67" : color);
  const label = card.status === "completed" ? `ran ${duration(ms)}` : card.status === "in_progress" ? duration(ms) : `queued ${duration(Date.now() - date(card.createdAt))}`;
  node.querySelector(".elapsed").textContent = label;
}

function render(snapshot) {
  const before = new Map([...document.querySelectorAll(".run-card")].map(node => [node.dataset.key, node.getBoundingClientRect()]));
  const all = [...snapshot.queued, ...snapshot.running, ...snapshot.completed];
  const incoming = new Set(all.map(card => card.key));
  const removed = [...state.cards.entries()].filter(([key]) => !incoming.has(key));
  const oldNodes = new Map(state.cards);
  state.previous = new Map(all.map(card => [card.key, state.snapshot ? [...state.snapshot.queued, ...state.snapshot.running, ...state.snapshot.completed].find(previous => previous.key === card.key) : null]));
  state.snapshot = snapshot;

  for (const [key, node] of removed) {
    const rect = node.getBoundingClientRect();
    const ghost = node.cloneNode(true);
    Object.assign(ghost.style, {position:"fixed", left:`${rect.left}px`, top:`${rect.top}px`, width:`${rect.width}px`, margin:"0", zIndex:"50", pointerEvents:"none"});
    document.body.appendChild(ghost);
    ghost.animate([{opacity:1, transform:"scale(1)"},{opacity:0, transform:"scale(.96)"}], {duration:220, easing:"ease-out"}).finished.finally(() => ghost.remove());
  }

  for (const lane of Object.values(lanes)) lane.replaceChildren();
  state.cards = new Map();
  for (const [laneName, cards] of Object.entries({queued:snapshot.queued, running:snapshot.running, completed:snapshot.completed})) {
    const groups = new Map();
    for (const card of cards) {
      const node = oldNodes.get(card.key) || makeCard(card);
      updateCard(node, card, state.previous.get(card.key));
      node.hidden = !cardMatches(card);
      const groupKey = `${card.owner}/${card.repo}/${card.runId}`;
      let group = groups.get(groupKey);
      if (!group) {
        group = makeGroup(card);
        groups.set(groupKey, group);
        lanes[laneName].appendChild(group);
      }
      group.querySelector(".group-jobs").appendChild(node);
      state.cards.set(card.key, node);
    }
    for (const group of groups.values()) group.hidden = ![...group.querySelectorAll(".run-card")].some(node => !node.hidden);
  }

  if (!reducedMotion) requestAnimationFrame(() => {
    for (const [key, node] of state.cards) {
      const first = before.get(key), last = node.getBoundingClientRect();
      if (!first) {
        node.animate([{opacity:0, transform:"translateY(-18px)"},{opacity:1, transform:"none"}], {duration:320, easing:"cubic-bezier(.22,1,.36,1)"});
      } else {
        const dx = first.left - last.left, dy = first.top - last.top;
        if (Math.abs(dx) > 1 || Math.abs(dy) > 1) node.animate([{transform:`translate(${dx}px,${dy}px) scale(1.025)`, boxShadow:"0 18px 48px rgba(0,0,0,.5)"},{transform:"none"}], {duration:520, easing:"cubic-bezier(.22,1,.36,1)"});
      }
    }
  });
  updateSummary();
  updateRepositories(snapshot.repositories, all);
  document.querySelector("#live-dot").classList.add("online", "pulse");
  setTimeout(() => document.querySelector("#live-dot").classList.remove("pulse"), 650);
  document.querySelector("#live-label").textContent = snapshot.repositories.some(repo => repo.stale) ? "LIVE · PARTIAL" : "LIVE";
}

function updateSummary() {
  if (!state.snapshot) return;
  const visible = lane => lane.filter(cardMatches).length;
  document.querySelector("#repo-count").textContent = state.snapshot.repositories.length;
  document.querySelector("#active-count").textContent = state.snapshot.queued.length + state.snapshot.running.length;
  document.querySelector("#failed-count").textContent = state.snapshot.completed.filter(card => failed(card.conclusion)).length;
  document.querySelector("#sync-age").textContent = age(state.snapshot.lastSuccessfulSync);
  const rate = state.snapshot.rateLimit;
  document.querySelector("#rate-limit").textContent = rate?.limit ? `API ${rate.remaining}/${rate.limit}` : "Last sync";
  document.querySelector("#queued-count").textContent = visible(state.snapshot.queued);
  document.querySelector("#running-count").textContent = visible(state.snapshot.running);
  document.querySelector("#completed-count").textContent = `${visible(state.snapshot.completed)} / ${state.snapshot.completedLimit || 20}`;

  const selectedHasCards = [...state.snapshot.queued, ...state.snapshot.running, ...state.snapshot.completed].some(card => !state.filters.repo || card.repo === state.filters.repo);
  const emptyText = state.filters.repo && !selectedHasCards ? `${state.filters.repo} is monitored, but has no run in the current completed window` : "No runs in this lane";
  for (const lane of Object.values(lanes)) lane.dataset.empty = emptyText;
}

function updateRepositories(repositories, cards) {
  const counts = new Map();
  for (const card of cards) counts.set(card.repo, (counts.get(card.repo) || 0) + 1);
  const setOptions = (select, values, current) => {
    const first = select.options[0].cloneNode(true);
    select.replaceChildren(first, ...values.sort().map(value => new Option(value, value)));
    select.value = current;
  };
  setOptions(document.querySelector("#repo-filter"), repositories.map(repo => repo.name), state.filters.repo);
  setOptions(document.querySelector("#trigger-filter"), [...new Set(cards.map(card => card.event))], state.filters.trigger);
  const fleet = document.querySelector("#repo-fleet");
  fleet.replaceChildren(...repositories.map(repo => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = `repo-pill${repo.stale ? " stale" : ""}${state.filters.repo === repo.name ? " selected" : ""}`;
    button.title = repo.error || `${repo.name}: ${counts.get(repo.name) || 0} cards in current window`;
    button.innerHTML = `<span class="repo-dot"></span><span class="repo-pill-name"></span><span class="repo-pill-count">${counts.get(repo.name) || 0} cards</span>`;
    button.querySelector(".repo-pill-name").textContent = repo.name;
    button.addEventListener("click", () => {
      state.filters.repo = state.filters.repo === repo.name ? "" : repo.name;
      document.querySelector("#repo-filter").value = state.filters.repo;
      applyFilters();
      updateRepositories(repositories, cards);
    });
    return button;
  }));
}

function applyFilters() {
  if (!state.snapshot) return;
  for (const [key, node] of state.cards) {
    const card = [...state.snapshot.queued, ...state.snapshot.running, ...state.snapshot.completed].find(item => item.key === key);
    node.hidden = !cardMatches(card);
  }
  for (const group of document.querySelectorAll(".run-group")) group.hidden = ![...group.querySelectorAll(".run-card")].some(node => !node.hidden);
  updateSummary();
}

async function loadSnapshot() {
  try {
    const response = await fetch("/api/snapshot", {cache:"no-store"});
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    render(await response.json());
  } catch (error) {
    document.querySelector("#live-label").textContent = "DISCONNECTED";
    document.querySelector("#live-dot").classList.remove("online");
  }
}

async function openDrawer(key) {
  const card = [...state.snapshot.queued, ...state.snapshot.running, ...state.snapshot.completed].find(item => item.key === key);
  if (!card) return;
  const drawer = document.querySelector("#drawer"), content = document.querySelector("#drawer-content"), backdrop = document.querySelector("#backdrop");
  drawer.classList.add("open"); drawer.setAttribute("aria-hidden", "false"); backdrop.hidden = false;
  content.innerHTML = `<div class="loading">Loading attempt history…</div>`;
  try {
    const response = await fetch(`/api/runs/${encodeURIComponent(card.owner)}/${encodeURIComponent(card.repo)}/${card.runId}`);
    const detail = await response.json();
    if (!response.ok) throw new Error(detail.error || `HTTP ${response.status}`);
    content.innerHTML = `<p class="eyebrow">${escapeHTML(card.repo)} · RUN #${card.runNumber}</p><h2>${escapeHTML(card.workflow)}</h2><p class="drawer-topic">${escapeHTML(card.topic)}</p><p><a class="github-link" href="${escapeHTML(card.url)}" target="_blank" rel="noopener">Open in GitHub ↗</a></p>${detail.attempts.map(renderAttempt).join("")}`;
  } catch (error) {
    content.innerHTML = `<p class="error-message">${escapeHTML(error.message)}</p>`;
  }
}

function renderAttempt(attempt) {
  const bad = failed(attempt.conclusion);
  const jobs = (attempt.jobs || []).map(job => `<details class="job"><summary><span>${escapeHTML(job.name)}</span><b>${escapeHTML(job.conclusion || job.status)}</b></summary><ul class="steps">${(job.steps || []).map(step => `<li><span>${escapeHTML(step.name)}</span><span>${escapeHTML(step.conclusion || step.status)}</span></li>`).join("")}</ul></details>`).join("");
  return `<section class="attempt ${bad ? "failed" : ""}"><header><strong>Attempt ${attempt.number}</strong><span>${escapeHTML(attempt.conclusion || attempt.status)}</span></header>${jobs || '<p class="loading">No jobs returned</p>'}</section>`;
}

function closeDrawer() {
  document.querySelector("#drawer").classList.remove("open");
  document.querySelector("#drawer").setAttribute("aria-hidden", "true");
  document.querySelector("#backdrop").hidden = true;
}

document.querySelector("#search").addEventListener("input", event => { state.filters.search = event.target.value.trim().toLowerCase(); applyFilters(); });
document.querySelector("#repo-filter").addEventListener("change", event => { state.filters.repo = event.target.value; applyFilters(); });
document.querySelector("#trigger-filter").addEventListener("change", event => { state.filters.trigger = event.target.value; applyFilters(); });
document.querySelector("#refresh").addEventListener("click", loadSnapshot);
document.querySelector("#drawer-close").addEventListener("click", closeDrawer);
document.querySelector("#backdrop").addEventListener("click", closeDrawer);
document.addEventListener("keydown", event => { if (event.key === "Escape") closeDrawer(); });
setInterval(() => {
  document.querySelector("#clock").textContent = new Date().toLocaleTimeString([], {hour12:false});
  if (state.snapshot) {
    for (const cards of [state.snapshot.queued, state.snapshot.running, state.snapshot.completed]) for (const card of cards) {
      const node = state.cards.get(card.key); if (node) updateElapsed(node, card);
    }
    document.querySelector("#sync-age").textContent = age(state.snapshot.lastSuccessfulSync);
  }
}, 1000);
loadSnapshot();
setInterval(loadSnapshot, 5000);
